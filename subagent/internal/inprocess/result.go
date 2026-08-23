package inprocess

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/assistantoutput"
)

func readResult(
	conversation *session.Session,
	boundary int64,
	cancelled bool,
	structured *structuredCapture,
) (subagent.Result, error) {
	if conversation == nil {
		return subagent.Result{}, errors.New("subagent: one-shot child Session is nil")
	}
	events := conversation.Events()
	if boundary < 0 || boundary > int64(len(events)) {
		return subagent.Result{}, errors.New("subagent: invalid one-shot activation boundary")
	}
	ownEvents := events[boundary:]
	consumed, consumedErr := agent.FoldConsumedWork(ownEvents)
	if consumedErr != nil {
		return subagent.Result{}, consumedErr
	}
	output, outputErr := assistantoutput.Select(ownEvents)
	if outputErr != nil {
		return subagent.Result{}, outputErr
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
	result := subagent.Result{
		Output:     output,
		StopReason: stopReason,
	}
	if diagnostic != "" {
		result.Diagnostic = stringPointer(diagnostic)
	}
	if structured != nil {
		result.Structured = structured.Captured()
		if len(result.Structured) == 0 && stopReason == subagent.StopCompleted {
			result.StopReason = subagent.StopError
			result.Diagnostic = stringPointer(
				"structured output was not recorded",
			)
		}
	}
	return result, nil
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
