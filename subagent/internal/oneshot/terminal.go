package oneshot

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

type executionTerminator struct {
	running     *sharedexecution.Execution
	executions  *sharedexecution.Registry
	handle      agent.Handle
	parent      agent.Agent
	seedBuilder string
	runID       subagent.RunID
	boundary    int64
	environment ChildEnvironment
	publisher   sharedexecution.EventPublisher
}

func (terminator *executionTerminator) Terminate(
	stopContext context.Context,
	cause sharedexecution.StopCause,
) (subagent.Terminal, error) {
	cancelled := cause != sharedexecution.StopNormal
	if cancelled {
		terminator.handle.Subject.Cancel(
			agent.DisposedCancel{},
			agent.CancelOptions{
				KeepInbox: false,
			},
		)
	}
	var terminalErr error
	if idleErr := terminator.handle.Subject.WhenIdle(stopContext); idleErr != nil {
		terminalErr = errors.Join(terminalErr, idleErr)
	}
	terminalValue, resultErr := readTerminal(
		terminator.handle.Subject.SessionValue(),
		terminator.boundary,
		cancelled,
		terminator.environment,
	)
	terminalErr = errors.Join(terminalErr, resultErr)
	if terminalErr != nil {
		terminalValue.StopReason = subagent.StopError
	}
	if terminator.publisher != nil {
		terminator.publisher.PublishEnded(
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
	if terminator.executions != nil {
		terminator.executions.Remove(terminator.running)
	}
	if cause != sharedexecution.StopExternal {
		if disposeErr := terminator.handle.Dispose(
			context.WithoutCancel(stopContext),
		); disposeErr != nil {
			terminalErr = errors.Join(terminalErr, disposeErr)
		}
	}
	return terminalValue, terminalErr
}

func readTerminal(
	conversation session.Context,
	boundary int64,
	cancelled bool,
	environment ChildEnvironment,
) (subagent.Terminal, error) {
	if conversation == nil {
		return subagent.Terminal{}, errors.New(
			"subagent: OneShot child Session is nil",
		)
	}
	events := conversation.Events()
	if boundary < 0 || boundary > int64(len(events)) {
		return subagent.Terminal{}, errors.New(
			"subagent: invalid OneShot execution boundary",
		)
	}
	ownEvents := events[boundary:]
	consumed, consumedErr := agent.FoldConsumedWork(ownEvents)
	if consumedErr != nil {
		return subagent.Terminal{}, consumedErr
	}
	output, outputErr := sharedexecution.SelectAssistantOutput(ownEvents)
	if outputErr != nil {
		return subagent.Terminal{}, outputErr
	}
	stopReason := subagent.StopError
	var diagnostic string
	if consumed.End != nil {
		stopReason = mapStopReason(consumed.End.Reason)
		if failure, matches := consumed.End.Reason.(session.TurnError); matches {
			diagnostic = failure.Error.Message
		}
	}
	if cancelled && stopReason != subagent.StopCompleted {
		stopReason = subagent.StopAborted
	}
	terminalValue := subagent.Terminal{
		Output:     output,
		StopReason: stopReason,
	}
	if diagnostic != "" {
		terminalValue.Diagnostic = stringPointer(diagnostic)
	}
	if structured, requested := environment.StructuredOutput(); requested {
		terminalValue.Structured = structured
		if len(terminalValue.Structured) == 0 &&
			stopReason == subagent.StopCompleted {
			terminalValue.StopReason = subagent.StopError
			terminalValue.Diagnostic = stringPointer(
				"structured output was not recorded",
			)
		}
	}
	return terminalValue, nil
}

func mapStopReason(reason session.TurnEndReason) subagent.StopReason {
	switch reason.TurnEndKind() {
	case "completed":
		return subagent.StopCompleted
	case "max-tokens":
		return subagent.StopMaxTokens
	case "aborted":
		return subagent.StopAborted
	case "blocked":
		return subagent.StopRefusal
	case "error", "interrupted":
		return subagent.StopError
	default:
		return subagent.StopError
	}
}

func stringPointer(value string) *string {
	return &value
}

var _ sharedexecution.Terminator = (*executionTerminator)(nil)
