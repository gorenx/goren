package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/internal/jsonvalue"
)

// StreamChunk is one provider-neutral raw streaming item.
type StreamChunk interface {
	ChunkType() string
	CloneChunk() (StreamChunk, error)
}

type BlockStartChunk struct {
	Type      string `json:"type"`
	Index     int    `json:"index"`
	BlockType string `json:"blockType"`
}

func (BlockStartChunk) ChunkType() string { return "block-start" }
func (entry BlockStartChunk) CloneChunk() (StreamChunk, error) {
	if entry.BlockType == "" {
		return nil, errors.New("llm: block-start needs a blockType")
	}
	entry.Type = "block-start"
	return entry, nil
}
func (entry BlockStartChunk) MarshalJSON() ([]byte, error) {
	type wireBlockStart BlockStartChunk
	entry.Type = "block-start"
	return json.Marshal(wireBlockStart(entry))
}

type TextDeltaChunk struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Text  string `json:"text"`
}

func (TextDeltaChunk) ChunkType() string { return "text-delta" }
func (entry TextDeltaChunk) CloneChunk() (StreamChunk, error) {
	entry.Type = "text-delta"
	return entry, nil
}
func (entry TextDeltaChunk) MarshalJSON() ([]byte, error) {
	type wireTextDelta TextDeltaChunk
	entry.Type = "text-delta"
	return json.Marshal(wireTextDelta(entry))
}

type ReasoningDeltaChunk struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Text  string `json:"text"`
}

func (ReasoningDeltaChunk) ChunkType() string { return "reasoning-delta" }
func (entry ReasoningDeltaChunk) CloneChunk() (StreamChunk, error) {
	entry.Type = "reasoning-delta"
	return entry, nil
}
func (entry ReasoningDeltaChunk) MarshalJSON() ([]byte, error) {
	type wireReasoningDelta ReasoningDeltaChunk
	entry.Type = "reasoning-delta"
	return json.Marshal(wireReasoningDelta(entry))
}

type ToolCallDeltaChunk struct {
	Type           string  `json:"type"`
	Index          int     `json:"index"`
	ID             CallID  `json:"id"`
	Name           *string `json:"name,omitempty"`
	ArgumentsDelta string  `json:"argumentsDelta"`
}

func (ToolCallDeltaChunk) ChunkType() string { return "tool-call-delta" }
func (entry ToolCallDeltaChunk) CloneChunk() (StreamChunk, error) {
	entry.Type = "tool-call-delta"
	if entry.Name != nil {
		detachedName := *entry.Name
		entry.Name = &detachedName
	}
	return entry, nil
}
func (entry ToolCallDeltaChunk) MarshalJSON() ([]byte, error) {
	detachedChunk, err := entry.CloneChunk()
	if err != nil {
		return nil, err
	}
	type wireToolCallDelta ToolCallDeltaChunk
	return json.Marshal(wireToolCallDelta(detachedChunk.(ToolCallDeltaChunk)))
}

type BlockEndChunk struct {
	Type  string       `json:"type"`
	Index int          `json:"index"`
	Block ContentBlock `json:"block"`
}

func (BlockEndChunk) ChunkType() string { return "block-end" }
func (entry BlockEndChunk) CloneChunk() (StreamChunk, error) {
	if entry.Block == nil {
		return nil, errors.New("llm: block-end needs a block")
	}
	detachedBlock, err := entry.Block.CloneContent()
	if err != nil {
		return nil, err
	}
	entry.Type = "block-end"
	entry.Block = detachedBlock
	return entry, nil
}
func (entry BlockEndChunk) MarshalJSON() ([]byte, error) {
	detachedChunk, err := entry.CloneChunk()
	if err != nil {
		return nil, err
	}
	type wireBlockEnd BlockEndChunk
	return json.Marshal(wireBlockEnd(detachedChunk.(BlockEndChunk)))
}

type UsageChunk struct {
	Type  string     `json:"type"`
	Usage TokenUsage `json:"usage"`
}

func (UsageChunk) ChunkType() string { return "usage" }
func (entry UsageChunk) CloneChunk() (StreamChunk, error) {
	entry.Type = "usage"
	entry.Usage = cloneUsage(entry.Usage)
	return entry, nil
}
func (entry UsageChunk) MarshalJSON() ([]byte, error) {
	type wireUsage UsageChunk
	entry.Type = "usage"
	entry.Usage = cloneUsage(entry.Usage)
	return json.Marshal(wireUsage(entry))
}

type FinishChunk struct {
	Type        string          `json:"type"`
	Reason      FinishReason    `json:"reason"`
	ReplayState json.RawMessage `json:"replayState,omitempty"`
}

func (FinishChunk) ChunkType() string { return "finish" }
func (entry FinishChunk) CloneChunk() (StreamChunk, error) {
	if entry.Reason == nil {
		return nil, errors.New("llm: finish chunk needs a reason")
	}
	detachedReason, err := entry.Reason.CloneReason()
	if err != nil {
		return nil, err
	}
	entry.Type = "finish"
	entry.Reason = detachedReason
	if len(entry.ReplayState) != 0 {
		entry.ReplayState, err = jsonvalue.Clone(entry.ReplayState)
		if err != nil {
			return nil, fmt.Errorf("llm: invalid finish replayState: %w", err)
		}
	}
	return entry, nil
}
func (entry FinishChunk) MarshalJSON() ([]byte, error) {
	detachedChunk, err := entry.CloneChunk()
	if err != nil {
		return nil, err
	}
	type wireFinish FinishChunk
	return json.Marshal(wireFinish(detachedChunk.(FinishChunk)))
}

// ChunkStream is the pull-based Go equivalent of AsyncIterable<StreamChunk>.
// It preserves ordering and lets the consumer exert backpressure.
type ChunkStream interface {
	Next(context.Context) (StreamChunk, bool, error)
	Close(context.Context) error
}

// IsTokenDelta reports whether a stream chunk contains non-empty model output.
func IsTokenDelta(entry StreamChunk) bool {
	switch typedChunk := entry.(type) {
	case TextDeltaChunk:
		return typedChunk.Text != ""
	case ReasoningDeltaChunk:
		return typedChunk.Text != ""
	case ToolCallDeltaChunk:
		return typedChunk.ArgumentsDelta != "" || typedChunk.Name != nil
	default:
		return false
	}
}
