package inprocess

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

type turnOutcome struct {
	kind       string
	diagnostic string
}

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
	outcome, found, outcomeErr := lastTurnOutcome(ownEvents)
	if outcomeErr != nil {
		return subagent.Result{}, outcomeErr
	}
	output, outputErr := lastAssistantOutput(ownEvents)
	if outputErr != nil {
		return subagent.Result{}, outputErr
	}
	stopReason := subagent.StopError
	if found {
		stopReason = mapStopReason(outcome.kind)
	}
	if cancelled && stopReason != subagent.StopCompleted {
		stopReason = subagent.StopAborted
	}
	result := subagent.Result{
		Output:     output,
		StopReason: stopReason,
	}
	if outcome.diagnostic != "" {
		result.Diagnostic = stringPointer(outcome.diagnostic)
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

func lastTurnOutcome(events []session.Event) (turnOutcome, bool, error) {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type != session.TurnEndEventName {
			continue
		}
		var wireValue struct {
			Reason struct {
				Kind  string `json:"kind"`
				Error struct {
					Message string `json:"message"`
				} `json:"error,omitempty"`
			} `json:"reason"`
		}
		if decodeErr := decodeJSON(events[index].Data, &wireValue); decodeErr != nil {
			return turnOutcome{}, false, fmt.Errorf(
				"subagent: decode turn/end at seq %d: %w",
				events[index].Seq,
				decodeErr,
			)
		}
		return turnOutcome{
			kind:       wireValue.Reason.Kind,
			diagnostic: wireValue.Reason.Error.Message,
		}, true, nil
	}
	return turnOutcome{}, false, nil
}

func lastAssistantOutput(events []session.Event) ([]llm.ContentBlock, error) {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type != session.AssistantMessageEventName {
			continue
		}
		var wireValue struct {
			Message json.RawMessage `json:"message"`
		}
		if decodeErr := decodeJSON(events[index].Data, &wireValue); decodeErr != nil {
			return nil, fmt.Errorf(
				"subagent: decode assistant/message at seq %d: %w",
				events[index].Seq,
				decodeErr,
			)
		}
		messageValue, messageErr := llm.DecodeMessage(wireValue.Message)
		if messageErr != nil {
			return nil, messageErr
		}
		assistantMessage, matches := messageValue.(llm.AssistantMessage)
		if !matches {
			return nil, errors.New(
				"subagent: assistant/message contains a non-assistant message",
			)
		}
		return assistantMessage.ContentValue(), nil
	}
	return nil, nil
}

func mapStopReason(kind string) subagent.StopReason {
	switch kind {
	case "completed":
		return subagent.StopCompleted
	case "max-tokens":
		return subagent.StopMaxTokens
	case "aborted":
		return subagent.StopAborted
	case "blocked":
		return subagent.StopRefusal
	default:
		return subagent.StopError
	}
}

func decodeJSON(rawValue json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	if decodeErr := decoder.Decode(target); decodeErr != nil {
		return decodeErr
	}
	var trailing json.RawMessage
	if decodeErr := decoder.Decode(&trailing); !errors.Is(decodeErr, io.EOF) {
		if decodeErr == nil {
			return errors.New("multiple JSON values")
		}
		return decodeErr
	}
	return nil
}

func stringPointer(value string) *string {
	return &value
}
