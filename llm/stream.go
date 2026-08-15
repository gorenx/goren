package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/internal/jsonvalue"
)

// FinishReason is the merge-extensible terminal outcome contract.
type FinishReason interface {
	ReasonKind() string
	CloneReason() (FinishReason, error)
}

type StopFinish struct {
	Kind string `json:"kind"`
}

func (StopFinish) ReasonKind() string { return "stop" }
func (reason StopFinish) CloneReason() (FinishReason, error) {
	reason.Kind = "stop"
	return reason, nil
}

type ToolCallsFinish struct {
	Kind string `json:"kind"`
}

func (ToolCallsFinish) ReasonKind() string { return "tool-calls" }
func (reason ToolCallsFinish) CloneReason() (FinishReason, error) {
	reason.Kind = "tool-calls"
	return reason, nil
}

type MaxTokensFinish struct {
	Kind string `json:"kind"`
}

func (MaxTokensFinish) ReasonKind() string { return "max-tokens" }
func (reason MaxTokensFinish) CloneReason() (FinishReason, error) {
	reason.Kind = "max-tokens"
	return reason, nil
}

type AbortedFinish struct {
	Kind    string     `json:"kind"`
	Failure LlmFailure `json:"failure"`
}

func (AbortedFinish) ReasonKind() string { return "aborted" }
func (reason AbortedFinish) CloneReason() (FinishReason, error) {
	if err := validateFailure(reason.Failure); err != nil {
		return nil, err
	}
	reason.Kind = "aborted"
	reason.Failure = cloneFailure(reason.Failure)
	return reason, nil
}

type ErrorFinish struct {
	Kind    string     `json:"kind"`
	Failure LlmFailure `json:"failure"`
}

func (ErrorFinish) ReasonKind() string { return "error" }
func (reason ErrorFinish) CloneReason() (FinishReason, error) {
	if err := validateFailure(reason.Failure); err != nil {
		return nil, err
	}
	reason.Kind = "error"
	reason.Failure = cloneFailure(reason.Failure)
	return reason, nil
}

// OpaqueFinishReason preserves a provider/plugin extension reason.
type OpaqueFinishReason struct {
	kindName string
	rawValue json.RawMessage
}

func NewOpaqueFinishReason(kindName string, rawValue json.RawMessage) (OpaqueFinishReason, error) {
	if kindName == "" {
		return OpaqueFinishReason{}, errors.New("llm: opaque finish kind is empty")
	}
	detached, err := jsonvalue.Clone(rawValue)
	if err != nil || !jsonvalue.IsObject(detached) {
		return OpaqueFinishReason{}, errors.New("llm: opaque finish reason must be a lossless JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(detached, &fields); err != nil {
		return OpaqueFinishReason{}, err
	}
	var encodedKind string
	if err := json.Unmarshal(fields["kind"], &encodedKind); err != nil || encodedKind != kindName {
		return OpaqueFinishReason{}, errors.New("llm: opaque finish discriminant does not match")
	}
	return OpaqueFinishReason{kindName: kindName, rawValue: detached}, nil
}

func (reason OpaqueFinishReason) ReasonKind() string { return reason.kindName }
func (reason OpaqueFinishReason) CloneReason() (FinishReason, error) {
	return NewOpaqueFinishReason(reason.kindName, reason.rawValue)
}
func (reason OpaqueFinishReason) MarshalJSON() ([]byte, error) {
	if reason.kindName == "" || len(reason.rawValue) == 0 {
		return nil, errors.New("llm: invalid opaque finish reason")
	}
	return append([]byte(nil), reason.rawValue...), nil
}

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

// DecodeFinishReason restores core terminal outcomes and preserves extensions.
func DecodeFinishReason(rawValue json.RawMessage) (FinishReason, error) {
	if !jsonvalue.IsObject(rawValue) {
		return nil, errors.New("llm: finish reason must be an object")
	}
	var header struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(rawValue, &header); err != nil || header.Kind == "" {
		return nil, errors.New("llm: finish reason kind is missing")
	}
	switch header.Kind {
	case "stop":
		var reason StopFinish
		if err := decodeStrict(rawValue, &reason); err != nil {
			return nil, err
		}
		return reason.CloneReason()
	case "tool-calls":
		var reason ToolCallsFinish
		if err := decodeStrict(rawValue, &reason); err != nil {
			return nil, err
		}
		return reason.CloneReason()
	case "max-tokens":
		var reason MaxTokensFinish
		if err := decodeStrict(rawValue, &reason); err != nil {
			return nil, err
		}
		return reason.CloneReason()
	case "aborted":
		var reason AbortedFinish
		if err := decodeStrict(rawValue, &reason); err != nil {
			return nil, err
		}
		return reason.CloneReason()
	case "error":
		var reason ErrorFinish
		if err := decodeStrict(rawValue, &reason); err != nil {
			return nil, err
		}
		return reason.CloneReason()
	default:
		return NewOpaqueFinishReason(header.Kind, rawValue)
	}
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

// ChunkStream is the pull-based Go equivalent of AsyncIterable<StreamChunk>.
// It preserves ordering and lets the consumer exert backpressure.
type ChunkStream interface {
	Next(context.Context) (StreamChunk, bool, error)
	Close(context.Context) error
}

type sliceChunkStream struct {
	mu      sync.Mutex
	entries []StreamChunk
	index   int
	closed  bool
}

// NewSliceStream snapshots a deterministic stream for tests and in-process adapters.
func NewSliceStream(entries []StreamChunk) (ChunkStream, error) {
	detached := make([]StreamChunk, len(entries))
	for index, entry := range entries {
		if entry == nil {
			return nil, fmt.Errorf("llm: stream chunk %d is nil", index)
		}
		copyValue, err := entry.CloneChunk()
		if err != nil {
			return nil, fmt.Errorf("llm: clone stream chunk %d: %w", index, err)
		}
		detached[index] = copyValue
	}
	return &sliceChunkStream{entries: detached}, nil
}

func (streamState *sliceChunkStream) Next(requestContext context.Context) (StreamChunk, bool, error) {
	if err := requestContext.Err(); err != nil {
		return nil, false, err
	}
	streamState.mu.Lock()
	defer streamState.mu.Unlock()
	if streamState.closed || streamState.index >= len(streamState.entries) {
		return nil, false, nil
	}
	entry := streamState.entries[streamState.index]
	streamState.index++
	copyValue, err := entry.CloneChunk()
	return copyValue, true, err
}

func (streamState *sliceChunkStream) Close(context.Context) error {
	streamState.mu.Lock()
	streamState.closed = true
	streamState.mu.Unlock()
	return nil
}
