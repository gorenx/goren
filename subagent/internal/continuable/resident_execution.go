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

// residentExecution is one published Continuable child Agent epoch and its
// common Execution. It owns settlement observation and terminal work for that
// exact epoch.
type residentExecution struct {
	owner       *continuableChild
	agents      agent.Registry
	descendants agent.RuntimeDescendants
	sessions    session.LiveStore
	publisher   sharedexecution.EventPublisher
	failures    FailureReporter
	executions  *sharedexecution.Registry
	handle      agent.Handle
	execution   *sharedexecution.Execution
	parent      agent.Agent
	seedBuilder string
	boundary    int64
	wake        chan struct{}
}

func (resident *residentExecution) notify() {
	select {
	case resident.wake <- struct{}{}:
	default:
	}
}

func (resident *residentExecution) watch() {
	go func() {
		for {
			idleContext, cancelIdle := context.WithCancel(context.Background())
			idleResult := make(chan error, 1)
			go func() {
				idleResult <- resident.handle.Subject.WhenIdle(idleContext)
			}()
			select {
			case <-resident.handle.ClosingSignal():
				cancelIdle()
				resident.execution.Stop(sharedexecution.StopExternal)
				return
			case <-resident.wake:
				cancelIdle()
				continue
			case <-idleResult:
				cancelIdle()
			}
			if resident.execution.State() != subagent.ExecutionActive {
				return
			}
			childAgent := resident.handle.Subject
			settled := childAgent.StatusValue() == agent.StatusIdle &&
				!childAgent.InboxValue().HasPending() &&
				!resident.descendants.HasRuntimeDescendants(childAgent)
			if settled {
				resident.execution.Stop(sharedexecution.StopIdle)
				return
			}
		}
	}()
}

// Terminate settles this exact resident epoch, reports its result, and
// releases the owned Agent Handle.
func (resident *residentExecution) Terminate(
	stopContext context.Context,
	cause sharedexecution.StopCause,
) (subagent.Terminal, error) {
	if cause != sharedexecution.StopIdle && cause != sharedexecution.StopNormal {
		resident.handle.Subject.Cancel(
			agent.ParentCancel{},
			agent.CancelOptions{
				KeepInbox: false,
			},
		)
	}
	var terminalErr error
	if err := resident.handle.Subject.WhenIdle(stopContext); err != nil {
		terminalErr = errors.Join(terminalErr, err)
	}
	if err := resident.sessions.Flush(
		context.WithoutCancel(stopContext),
		resident.handle.Subject.SessionValue(),
	); err != nil {
		resident.failures.ReportFinalFlushFailure(
			FinalFlushFailure{
				ChildID: resident.handle.Subject.ID(),
				Error:   err,
			},
		)
	}
	fallback := subagent.StopAborted
	if cause == sharedexecution.StopIdle || cause == sharedexecution.StopNormal {
		fallback = subagent.StopCompleted
	}
	executionEvents, err := currentExecutionEvents(
		resident.handle.Subject.SessionValue(),
		resident.boundary,
	)
	if err != nil {
		terminalErr = errors.Join(terminalErr, err)
	}
	terminalValue := subagent.Terminal{
		StopReason: executionStopReason(executionEvents, fallback),
	}
	output, err := sharedexecution.SelectAssistantOutput(executionEvents)
	if err != nil {
		terminalErr = errors.Join(terminalErr, err)
	} else {
		terminalValue.Output = output
	}
	if terminalErr != nil {
		terminalValue.StopReason = subagent.StopError
		terminalValue.Output = nil
	}
	resident.notifyParent(terminalValue)
	if resident.publisher != nil {
		resident.publisher.PublishEnded(
			resident.parent,
			subagent.Ended{
				RunID:                resident.execution.RunID(),
				Provider:             resident.seedBuilder,
				ID:                   resident.handle.Subject.ID(),
				Local:                true,
				StopReason:           terminalValue.StopReason,
				LastAssistantMessage: terminalValue.Output,
			},
		)
	}
	resident.executions.Remove(resident.execution)
	if cause != sharedexecution.StopExternal {
		if err = resident.handle.Dispose(
			context.WithoutCancel(stopContext),
		); err != nil {
			terminalErr = errors.Join(terminalErr, err)
		}
	}
	resident.owner.executionClosed(resident)
	resident.owner.registry.notify(resident.parent.ID())
	return terminalValue, terminalErr
}

func (resident *residentExecution) notifyParent(
	terminalValue subagent.Terminal,
) {
	parentAgent, found := resident.agents.Get(resident.parent.ID())
	if !found {
		return
	}
	summary := settlementSummary(
		resident.handle.Subject.ID(),
		terminalValue.StopReason,
	)
	content := []agentmessage.ContentBlock{
		agentmessage.NewTextBlock(summary),
	}
	if len(terminalValue.Output) == 0 {
		content = append(
			content,
			agentmessage.NewTextBlock("It left no closing message."),
		)
	} else {
		content = append(
			content,
			agentmessage.NewTextBlock("Its closing message:"),
		)
		content = append(content, terminalValue.Output...)
	}
	messageValue, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: content,
			Source: subagent.SettlementSource{
				Summary:         summary,
				SenderSessionID: resident.handle.Subject.ID(),
			},
		},
	)
	if err != nil {
		return
	}
	if parentAgent.StatusValue() == agent.StatusIdle {
		_ = parentAgent.Followup(messageValue)
	} else {
		_ = parentAgent.Steer(messageValue)
	}
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
	events []session.Event,
	fallback subagent.StopReason,
) subagent.StopReason {
	work, err := agent.FoldConsumedWork(events)
	if err != nil {
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

func currentExecutionEvents(
	conversation session.Context,
	boundary int64,
) ([]session.Event, error) {
	if conversation == nil {
		return nil, errors.New("subagent: Continuable child Session is nil")
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
	return suffix, nil
}

var _ sharedexecution.Terminator = (*residentExecution)(nil)
