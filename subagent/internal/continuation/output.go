package continuation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func lastAssistant(
	conversation *session.Session,
	boundary int64,
) ([]llm.ContentBlock, error) {
	if conversation == nil {
		return nil, errors.New("subagent: child Session is nil")
	}
	events := conversation.Events()
	for eventIndex := len(events) - 1; eventIndex >= 0; eventIndex-- {
		committed := events[eventIndex]
		if committed.Seq < boundary {
			break
		}
		if committed.Type != session.AssistantMessageEventName {
			continue
		}
		var wireValue struct {
			Turn    int64           `json:"turn"`
			Step    int64           `json:"step"`
			Message json.RawMessage `json:"message"`
			Usage   json.RawMessage `json:"usage,omitempty"`
		}
		if err := decodeOutputEvent(committed.Data, &wireValue); err != nil {
			return nil, fmt.Errorf(
				"subagent: decode assistant/message at Session seq %d: %w",
				committed.Seq,
				err,
			)
		}
		if wireValue.Turn < 1 || wireValue.Step < 1 {
			return nil, fmt.Errorf(
				"subagent: invalid assistant/message position at Session seq %d",
				committed.Seq,
			)
		}
		messageValue, err := llm.DecodeMessage(wireValue.Message)
		if err != nil {
			return nil, err
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

func decodeOutputEvent(rawValue json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
