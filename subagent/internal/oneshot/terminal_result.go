package oneshot

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

func readTerminal(
	conversation session.Context,
	boundary int64,
	cancelled bool,
	structured *structuredOutput,
) (subagent.Terminal, error) {
	if conversation == nil {
		return subagent.Terminal{}, errors.New("subagent: OneShot child Session is nil")
	}
	events := conversation.Events()
	if boundary < 0 || boundary > int64(len(events)) {
		return subagent.Terminal{}, errors.New("subagent: invalid OneShot execution boundary")
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
	if structured != nil {
		terminalValue.Structured = structured.Captured()
		if len(terminalValue.Structured) == 0 && stopReason == subagent.StopCompleted {
			terminalValue.StopReason = subagent.StopError
			terminalValue.Diagnostic = stringPointer("structured output was not recorded")
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

func stringPointer(value string) *string { return &value }
