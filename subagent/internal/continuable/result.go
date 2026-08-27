package continuable

import (
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

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
