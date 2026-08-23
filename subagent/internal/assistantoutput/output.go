// Package assistantoutput selects one subagent epoch's canonical final output.
package assistantoutput

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// Select returns the last non-empty assistant message, or accumulated text
// deltas when no such message was committed.
func Select(events []session.Event) ([]llm.ContentBlock, error) {
	var message []llm.ContentBlock
	var partial strings.Builder
	for _, committed := range events {
		switch committed.Type {
		case session.AssistantMessageEventName:
			content, decodeErr := decodeMessage(committed)
			if decodeErr != nil {
				return nil, decodeErr
			}
			if len(content) != 0 {
				message = content
			}
		case session.AssistantChunkEventName:
			text, decodeErr := decodeTextDelta(committed)
			if decodeErr != nil {
				return nil, decodeErr
			}
			partial.WriteString(text)
		}
	}
	if message != nil {
		return message, nil
	}
	if partial.Len() == 0 {
		return nil, nil
	}
	return []llm.ContentBlock{
		llm.NewTextBlock(partial.String()),
	}, nil
}

func decodeMessage(committed session.Event) ([]llm.ContentBlock, error) {
	var wireValue struct {
		Turn    int64           `json:"turn"`
		Step    int64           `json:"step"`
		Message json.RawMessage `json:"message"`
		Usage   json.RawMessage `json:"usage,omitempty"`
	}
	if decodeErr := decodeEvent(committed.Data, &wireValue); decodeErr != nil {
		return nil, fmt.Errorf(
			"subagent: decode assistant/message at Session seq %d: %w",
			committed.Seq,
			decodeErr,
		)
	}
	if wireValue.Turn < 1 || wireValue.Step < 1 {
		return nil, fmt.Errorf(
			"subagent: invalid assistant/message position at Session seq %d",
			committed.Seq,
		)
	}
	messageValue, messageErr := llm.DecodeMessage(wireValue.Message)
	if messageErr != nil {
		return nil, fmt.Errorf(
			"subagent: decode assistant/message payload at Session seq %d: %w",
			committed.Seq,
			messageErr,
		)
	}
	assistantMessage, matches := messageValue.(llm.AssistantMessage)
	if !matches {
		return nil, errors.New(
			"subagent: assistant/message contains a non-assistant message",
		)
	}
	return assistantMessage.ContentValue(), nil
}

func decodeTextDelta(committed session.Event) (string, error) {
	var wireValue struct {
		Turn  int64           `json:"turn"`
		Step  int64           `json:"step"`
		Chunk json.RawMessage `json:"chunk"`
	}
	if decodeErr := decodeEvent(committed.Data, &wireValue); decodeErr != nil {
		return "", fmt.Errorf(
			"subagent: decode assistant/chunk at Session seq %d: %w",
			committed.Seq,
			decodeErr,
		)
	}
	if wireValue.Turn < 1 || wireValue.Step < 1 {
		return "", fmt.Errorf(
			"subagent: invalid assistant/chunk position at Session seq %d",
			committed.Seq,
		)
	}
	chunkValue, chunkErr := llm.DecodeStreamChunk(wireValue.Chunk)
	if chunkErr != nil {
		return "", fmt.Errorf(
			"subagent: decode assistant/chunk payload at Session seq %d: %w",
			committed.Seq,
			chunkErr,
		)
	}
	switch textDelta := chunkValue.(type) {
	case llm.TextDeltaChunk:
		return textDelta.Text, nil
	case *llm.TextDeltaChunk:
		if textDelta != nil {
			return textDelta.Text, nil
		}
	}
	return "", nil
}

func decodeEvent(rawValue json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.DisallowUnknownFields()
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
