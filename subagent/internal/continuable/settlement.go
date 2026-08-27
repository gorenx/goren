package continuable

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

type executionTerminator struct {
	owner       *Service
	current     *currentExecution
	handle      agent.Handle
	parent      agent.Agent
	seedBuilder string
	runID       subagent.RunID
	boundary    int64
}

func (owner *Service) watch(current *currentExecution) {
	go func() {
		for {
			idleContext, cancelIdle := context.WithCancel(context.Background())
			idleResult := make(chan error, 1)
			go func() {
				idleResult <- current.terminator.handle.Subject.WhenIdle(
					idleContext,
				)
			}()
			current.slot.mutex.Lock()
			wakeSignal := current.wake
			current.slot.mutex.Unlock()
			select {
			case <-current.terminator.handle.ClosingSignal():
				cancelIdle()
				current.running.Stop(sharedexecution.StopExternal)
				return
			case <-wakeSignal:
				cancelIdle()
				continue
			case <-idleResult:
				cancelIdle()
			}
			current.slot.mutex.Lock()
			if current.slot.current != current ||
				current.running.State() != subagent.ExecutionActive {
				current.slot.mutex.Unlock()
				return
			}
			childAgent := current.terminator.handle.Subject
			settled := childAgent.StatusValue() == agent.StatusIdle &&
				!childAgent.InboxValue().HasPending() &&
				!owner.dependencies.Descendants.HasRuntimeDescendants(childAgent)
			if settled {
				current.running.Stop(sharedexecution.StopIdle)
				current.slot.mutex.Unlock()
				return
			}
			current.slot.mutex.Unlock()
		}
	}()
}

func (terminator *executionTerminator) Terminate(
	stopContext context.Context,
	cause sharedexecution.StopCause,
) (subagent.Terminal, error) {
	if cause != sharedexecution.StopIdle && cause != sharedexecution.StopNormal {
		terminator.handle.Subject.Cancel(
			agent.ParentCancel{},
			agent.CancelOptions{
				KeepInbox: false,
			},
		)
	}
	var terminalErr error
	if idleErr := terminator.handle.Subject.WhenIdle(stopContext); idleErr != nil {
		terminalErr = errors.Join(terminalErr, idleErr)
	}
	if flushErr := terminator.owner.dependencies.Sessions.Flush(
		context.WithoutCancel(stopContext),
		terminator.handle.Subject.SessionValue(),
	); flushErr != nil {
		terminator.owner.dependencies.Failures.ReportFinalFlushFailure(
			FinalFlushFailure{
				ChildID: terminator.handle.Subject.ID(),
				Error:   flushErr,
			},
		)
	}
	fallback := subagent.StopAborted
	if cause == sharedexecution.StopIdle || cause == sharedexecution.StopNormal {
		fallback = subagent.StopCompleted
	}
	terminalValue := subagent.Terminal{
		StopReason: executionStopReason(
			terminator.handle.Subject.SessionValue(),
			terminator.boundary,
			fallback,
		),
	}
	output, outputErr := lastAssistant(
		terminator.handle.Subject.SessionValue(),
		terminator.boundary,
	)
	if outputErr != nil {
		terminalErr = errors.Join(terminalErr, outputErr)
	} else {
		terminalValue.Output = output
	}
	if terminalErr != nil {
		terminalValue.StopReason = subagent.StopError
		terminalValue.Output = nil
	}
	terminator.notifyParent(terminalValue)
	if terminator.owner.dependencies.Publisher != nil {
		terminator.owner.dependencies.Publisher.PublishEnded(
			terminator.parent,
			subagent.Ended{
				RunID:                terminator.runID,
				Provider:             terminator.seedBuilder,
				ID:                   terminator.handle.Subject.ID(),
				Local:                true,
				StopReason:           terminalValue.StopReason,
				LastAssistantMessage: terminalValue.Output,
			},
		)
	}
	terminator.owner.dependencies.Executions.Remove(
		terminator.current.running,
	)
	if cause != sharedexecution.StopExternal {
		if disposeErr := terminator.handle.Dispose(
			context.WithoutCancel(stopContext),
		); disposeErr != nil {
			terminalErr = errors.Join(terminalErr, disposeErr)
		}
	}
	terminator.owner.detach(
		terminator.handle.Subject.ID(),
		terminator.current,
	)
	terminator.owner.wakeParent(terminator.parent.ID())
	return terminalValue, terminalErr
}

func (terminator *executionTerminator) notifyParent(
	terminalValue subagent.Terminal,
) {
	parentAgent, found := terminator.owner.dependencies.Agents.Get(
		terminator.parent.ID(),
	)
	if !found {
		return
	}
	summary := settlementSummary(
		terminator.handle.Subject.ID(),
		terminalValue.StopReason,
	)
	content := []agentmessage.ContentBlock{
		agentmessage.NewTextBlock(summary),
	}
	if len(terminalValue.Output) == 0 {
		content = append(content, agentmessage.NewTextBlock("It left no closing message."))
	} else {
		content = append(content, agentmessage.NewTextBlock("Its closing message:"))
		content = append(content, terminalValue.Output...)
	}
	messageValue, messageErr := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: content,
			Source: subagent.SettlementSource{
				Summary:         summary,
				SenderSessionID: terminator.handle.Subject.ID(),
			},
		},
	)
	if messageErr != nil {
		return
	}
	if parentAgent.StatusValue() == agent.StatusIdle {
		_ = parentAgent.Followup(messageValue)
	} else {
		_ = parentAgent.Steer(messageValue)
	}
}

func (owner *Service) wakeParent(parentID session.SessionID) {
	owner.mutex.Lock()
	slot := owner.slots[parentID]
	owner.mutex.Unlock()
	if slot == nil {
		return
	}
	slot.mutex.Lock()
	if slot.current != nil {
		signal(slot.current)
	}
	slot.mutex.Unlock()
}

func signal(current *currentExecution) {
	select {
	case <-current.wake:
	default:
		close(current.wake)
	}
	current.wake = make(chan struct{})
}

func settlementSummary(
	childID session.SessionID,
	reason subagent.StopReason,
) string {
	subject := fmt.Sprintf("Background subagent %s", childID)
	switch reason {
	case subagent.StopCompleted:
		return subject + " finished and can be resumed by another message."
	case subagent.StopAborted:
		return subject + " was stopped before it finished."
	case subagent.StopMaxTokens:
		return subject + " ran out of room before it finished."
	case subagent.StopRefusal:
		return subject + " declined the task."
	default:
		return subject + " failed before it finished."
	}
}

func executionStopReason(
	conversation session.Context,
	boundary int64,
	fallback subagent.StopReason,
) subagent.StopReason {
	if conversation == nil {
		return fallback
	}
	events := conversation.Events()
	suffix := make([]session.Event, 0, len(events))
	for _, committed := range events {
		if committed.Seq >= boundary {
			suffix = append(suffix, committed)
		}
	}
	work, foldErr := agent.FoldConsumedWork(suffix)
	if foldErr != nil {
		return subagent.StopError
	}
	if work.End == nil {
		if work.DroppedUnrun {
			return subagent.StopAborted
		}
		return fallback
	}
	switch work.End.Reason.TurnEndKind() {
	case "completed":
		if work.DroppedUnrun {
			return subagent.StopAborted
		}
		return subagent.StopCompleted
	case "blocked":
		return subagent.StopRefusal
	case "max-tokens":
		return subagent.StopMaxTokens
	case "interrupted", "aborted":
		return subagent.StopAborted
	case "error":
		return subagent.StopError
	default:
		return subagent.StopError
	}
}

func lastAssistant(
	conversation session.Context,
	boundary int64,
) ([]agentmessage.ContentBlock, error) {
	if conversation == nil {
		return nil, errors.New("subagent: child Session is nil")
	}
	events := conversation.Events()
	if boundary < 0 || boundary > conversation.Seq() {
		return nil, fmt.Errorf(
			"subagent: invalid Continuable execution boundary %d",
			boundary,
		)
	}
	suffix := make([]session.Event, 0, len(events))
	for _, committed := range events {
		if committed.Seq >= boundary {
			suffix = append(suffix, committed)
		}
	}
	return sharedexecution.SelectAssistantOutput(suffix)
}

var _ sharedexecution.Terminator = (*executionTerminator)(nil)
