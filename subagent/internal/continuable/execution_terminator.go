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
	owner       *residentExecutions
	current     *currentExecution
	handle      agent.Handle
	parent      agent.Agent
	seedBuilder string
	runID       subagent.RunID
	boundary    int64
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
	if flushErr := terminator.owner.sessions.Flush(
		context.WithoutCancel(stopContext),
		terminator.handle.Subject.SessionValue(),
	); flushErr != nil {
		terminator.owner.failures.ReportFinalFlushFailure(
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
	executionEvents, eventsErr := currentExecutionEvents(
		terminator.handle.Subject.SessionValue(),
		terminator.boundary,
	)
	if eventsErr != nil {
		terminalErr = errors.Join(terminalErr, eventsErr)
	}
	terminalValue := subagent.Terminal{
		StopReason: executionStopReason(executionEvents, fallback),
	}
	output, outputErr := sharedexecution.SelectAssistantOutput(executionEvents)
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
	if terminator.owner.publisher != nil {
		terminator.owner.publisher.PublishEnded(
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
	terminator.owner.executions.Remove(
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
	parentAgent, found := terminator.owner.agents.Get(
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
	work, foldErr := agent.FoldConsumedWork(events)
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

var _ sharedexecution.Terminator = (*executionTerminator)(nil)
