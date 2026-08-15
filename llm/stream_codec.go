package llm

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/internal/jsonvalue"
)

// UnmarshalJSON restores the interface-valued block of a block-end chunk.
func (entry *BlockEndChunk) UnmarshalJSON(rawValue []byte) error {
	if entry == nil {
		return errors.New("llm: cannot decode block-end into nil target")
	}
	var wireValue struct {
		Type  string          `json:"type"`
		Index int             `json:"index"`
		Block json.RawMessage `json:"block"`
	}
	if err := decodeStrict(rawValue, &wireValue); err != nil {
		return err
	}
	if wireValue.Type != "block-end" {
		return errors.New("llm: invalid block-end discriminant")
	}
	detachedBlock, err := decodeContentBlock(wireValue.Block)
	if err != nil {
		return err
	}
	*entry = BlockEndChunk{Type: "block-end", Index: wireValue.Index, Block: detachedBlock}
	return nil
}

// UnmarshalJSON restores the interface-valued reason of a finish chunk.
func (entry *FinishChunk) UnmarshalJSON(rawValue []byte) error {
	if entry == nil {
		return errors.New("llm: cannot decode finish into nil target")
	}
	var wireValue struct {
		Type        string          `json:"type"`
		Reason      json.RawMessage `json:"reason"`
		ReplayState json.RawMessage `json:"replayState,omitempty"`
	}
	if err := decodeStrict(rawValue, &wireValue); err != nil {
		return err
	}
	if wireValue.Type != "finish" {
		return errors.New("llm: invalid finish discriminant")
	}
	detachedReason, err := DecodeFinishReason(wireValue.Reason)
	if err != nil {
		return err
	}
	candidate := FinishChunk{Type: "finish", Reason: detachedReason, ReplayState: wireValue.ReplayState}
	detachedChunk, err := candidate.CloneChunk()
	if err != nil {
		return err
	}
	*entry = detachedChunk.(FinishChunk)
	return nil
}

// DecodeStreamChunk restores one durable raw-stream item.
func DecodeStreamChunk(rawValue json.RawMessage) (StreamChunk, error) {
	if !jsonvalue.IsObject(rawValue) {
		return nil, errors.New("llm: stream chunk must be an object")
	}
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(rawValue, &header); err != nil || header.Type == "" {
		return nil, errors.New("llm: stream chunk type is missing")
	}
	switch header.Type {
	case "block-start":
		var entry BlockStartChunk
		if err := decodeStrict(rawValue, &entry); err != nil {
			return nil, err
		}
		return entry.CloneChunk()
	case "text-delta":
		var entry TextDeltaChunk
		if err := decodeStrict(rawValue, &entry); err != nil {
			return nil, err
		}
		return entry.CloneChunk()
	case "reasoning-delta":
		var entry ReasoningDeltaChunk
		if err := decodeStrict(rawValue, &entry); err != nil {
			return nil, err
		}
		return entry.CloneChunk()
	case "tool-call-delta":
		var entry ToolCallDeltaChunk
		if err := decodeStrict(rawValue, &entry); err != nil {
			return nil, err
		}
		return entry.CloneChunk()
	case "block-end":
		var entry BlockEndChunk
		if err := json.Unmarshal(rawValue, &entry); err != nil {
			return nil, err
		}
		return entry.CloneChunk()
	case "usage":
		var entry UsageChunk
		if err := decodeStrict(rawValue, &entry); err != nil {
			return nil, err
		}
		return entry.CloneChunk()
	case "finish":
		var entry FinishChunk
		if err := json.Unmarshal(rawValue, &entry); err != nil {
			return nil, err
		}
		return entry.CloneChunk()
	default:
		return nil, fmt.Errorf("llm: unsupported stream chunk type %q", header.Type)
	}
}
