package tokenmeter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

type assistantMessageFacts struct {
	turn  int64
	step  int64
	usage *llm.TokenUsage
}

type assistantChunkFacts struct {
	turn  int64
	step  int64
	chunk llm.StreamChunk
}

func decodePayload(entry session.Event, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(entry.Data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf(
			"tokenmeter: decode %s at seq %d: %w",
			entry.Type,
			entry.Seq,
			err,
		)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf(
				"tokenmeter: %s at seq %d has trailing JSON",
				entry.Type,
				entry.Seq,
			)
		}
		return err
	}
	return nil
}

func decodeAssistantMessageFacts(entry session.Event) (assistantMessageFacts, error) {
	var wireValue struct {
		Turn    int64           `json:"turn"`
		Step    int64           `json:"step"`
		Message json.RawMessage `json:"message"`
		Usage   *llm.TokenUsage `json:"usage,omitempty"`
	}
	if err := decodePayload(entry, &wireValue); err != nil {
		return assistantMessageFacts{}, err
	}
	messageValue, err := llm.DecodeMessage(wireValue.Message)
	if err != nil {
		return assistantMessageFacts{}, err
	}
	_, valid := messageValue.(llm.AssistantMessage)
	if !valid {
		return assistantMessageFacts{}, fmt.Errorf(
			"tokenmeter: assistant/message at seq %d contains another role",
			entry.Seq,
		)
	}
	if wireValue.Usage != nil {
		if err := validateUsage(*wireValue.Usage); err != nil {
			return assistantMessageFacts{}, fmt.Errorf(
				"tokenmeter: assistant/message at seq %d usage: %w",
				entry.Seq,
				err,
			)
		}
	}
	return assistantMessageFacts{
		turn:  wireValue.Turn,
		step:  wireValue.Step,
		usage: cloneUsage(wireValue.Usage),
	}, nil
}

func decodeAssistantChunkFacts(entry session.Event) (assistantChunkFacts, error) {
	var wireValue struct {
		Turn  int64           `json:"turn"`
		Step  int64           `json:"step"`
		Chunk json.RawMessage `json:"chunk"`
	}
	if err := decodePayload(entry, &wireValue); err != nil {
		return assistantChunkFacts{}, err
	}
	chunkValue, err := llm.DecodeStreamChunk(wireValue.Chunk)
	if err != nil {
		return assistantChunkFacts{}, err
	}
	return assistantChunkFacts{
		turn:  wireValue.Turn,
		step:  wireValue.Step,
		chunk: chunkValue,
	}, nil
}

func validateUsage(usageValue llm.TokenUsage) error {
	values := []struct {
		name  string
		value *int64
	}{
		{
			name:  "inputTokens",
			value: &usageValue.InputTokens,
		},
		{
			name:  "outputTokens",
			value: &usageValue.OutputTokens,
		},
		{
			name:  "cacheReadTokens",
			value: usageValue.CacheReadTokens,
		},
		{
			name:  "cacheWriteTokens",
			value: usageValue.CacheWriteTokens,
		},
		{
			name:  "reasoningTokens",
			value: usageValue.ReasoningTokens,
		},
	}
	for _, candidate := range values {
		if candidate.value != nil &&
			(*candidate.value < 0 || *candidate.value > maxSafeTokenCount) {
			return fmt.Errorf("%s must be a non-negative safe integer", candidate.name)
		}
	}
	return nil
}

func cloneUsage(source *llm.TokenUsage) *llm.TokenUsage {
	if source == nil {
		return nil
	}
	detached := *source
	detached.CacheReadTokens = cloneInt64(source.CacheReadTokens)
	detached.CacheWriteTokens = cloneInt64(source.CacheWriteTokens)
	detached.ReasoningTokens = cloneInt64(source.ReasoningTokens)
	return &detached
}

func cloneInt64(source *int64) *int64 {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}
