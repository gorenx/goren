package llmdeepseek

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gorenx/goren/llm"
)

type openBlock struct {
	index  int
	kind   string
	text   string
	callID llm.CallID
	name   *string
}

type translatedStream struct {
	upstream payloadStream

	nextMu sync.Mutex
	mu     sync.Mutex
	closed bool
	done   bool
	queue  []llm.StreamChunk

	nextIndex      int
	textBlock      *openBlock
	reasoningBlock *openBlock
	toolBlocks     map[int]*openBlock
	order          []*openBlock
	pendingFinish  llm.FinishReason
	pendingUsage   *llm.TokenUsage
}

func translatePayloads(upstream payloadStream) (llm.ChunkStream, error) {
	if upstream == nil {
		return nil, fmt.Errorf("llm-deepseek: payload stream is nil")
	}
	return &translatedStream{upstream: upstream, toolBlocks: make(map[int]*openBlock)}, nil
}

func (streamState *translatedStream) Next(requestContext context.Context) (llm.StreamChunk, bool, error) {
	streamState.nextMu.Lock()
	defer streamState.nextMu.Unlock()
	for {
		streamState.mu.Lock()
		if len(streamState.queue) > 0 {
			entry := streamState.queue[0]
			streamState.queue = streamState.queue[1:]
			streamState.mu.Unlock()
			return entry, true, nil
		}
		if streamState.closed || streamState.done {
			streamState.mu.Unlock()
			return nil, false, nil
		}
		streamState.mu.Unlock()

		payload, available, err := streamState.upstream.NextPayload(requestContext)
		if err != nil {
			_ = streamState.upstream.Close(context.Background())
			return nil, false, err
		}
		if !available {
			_ = streamState.upstream.Close(context.Background())
			return nil, false, llm.MustLlmError("SSE payload stream ended without [DONE]", "STREAM_CLOSED")
		}
		if err := streamState.accept(payload); err != nil {
			_ = streamState.upstream.Close(context.Background())
			return nil, false, err
		}
	}
}

func (streamState *translatedStream) accept(payload string) error {
	streamState.mu.Lock()
	defer streamState.mu.Unlock()
	if payload == donePayload {
		for _, block := range streamState.order {
			streamState.queue = append(streamState.queue, llm.BlockEndChunk{
				Type: "block-end", Index: block.index, Block: closeTranslatedBlock(block),
			})
		}
		if streamState.pendingUsage != nil {
			streamState.queue = append(streamState.queue, llm.UsageChunk{Type: "usage", Usage: *streamState.pendingUsage})
		}
		finish := streamState.pendingFinish
		if finish == nil {
			finish = llm.StopFinish{Kind: "stop"}
		}
		if finish.ReasonKind() == "stop" && len(streamState.order) == 0 {
			finish = llm.ErrorFinish{Kind: "error", Failure: llm.LlmFailure{
				Message: "model returned a completed response with no content",
				Code:    llm.EmptyResponseCode,
			}}
		}
		streamState.queue = append(streamState.queue, llm.FinishChunk{Type: "finish", Reason: finish})
		streamState.done = true
		_ = streamState.upstream.Close(context.Background())
		return nil
	}

	var chunk wireChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return llm.MustLlmError(
			fmt.Sprintf("malformed SSE payload: %s", truncatePayload(payload, 120)),
			"MALFORMED_RESPONSE",
		)
	}
	for _, choice := range chunk.Choices {
		if choice == nil {
			return errors.New("llm-deepseek: provider chunk contains a null choice")
		}
		if choice.Delta != nil {
			streamState.acceptDelta(*choice.Delta)
		}
		if choice.FinishReason != nil {
			streamState.pendingFinish = MapFinishReason(*choice.FinishReason)
		}
	}
	if chunk.Usage != nil {
		usage := MapUsage(*chunk.Usage)
		streamState.pendingUsage = &usage
	}
	return nil
}

func (streamState *translatedStream) acceptDelta(delta wireDelta) {
	if delta.ReasoningContent != nil && *delta.ReasoningContent != "" {
		if streamState.reasoningBlock == nil {
			streamState.reasoningBlock = streamState.open("reasoning")
			streamState.queue = append(streamState.queue, llm.BlockStartChunk{
				Type: "block-start", Index: streamState.reasoningBlock.index, BlockType: "reasoning",
			})
		}
		streamState.reasoningBlock.text += *delta.ReasoningContent
		streamState.queue = append(streamState.queue, llm.ReasoningDeltaChunk{
			Type: "reasoning-delta", Index: streamState.reasoningBlock.index, Text: *delta.ReasoningContent,
		})
	}
	if delta.Content != nil && *delta.Content != "" {
		if streamState.textBlock == nil {
			streamState.textBlock = streamState.open("text")
			streamState.queue = append(streamState.queue, llm.BlockStartChunk{
				Type: "block-start", Index: streamState.textBlock.index, BlockType: "text",
			})
		}
		streamState.textBlock.text += *delta.Content
		streamState.queue = append(streamState.queue, llm.TextDeltaChunk{
			Type: "text-delta", Index: streamState.textBlock.index, Text: *delta.Content,
		})
	}
	for _, callDelta := range delta.ToolCalls {
		block := streamState.toolBlocks[callDelta.Index]
		if block == nil {
			block = streamState.open("tool-call")
			streamState.toolBlocks[callDelta.Index] = block
			streamState.queue = append(streamState.queue, llm.BlockStartChunk{
				Type: "block-start", Index: block.index, BlockType: "tool-call",
			})
		}
		if callDelta.ID != nil {
			block.callID = llm.CallID(*callDelta.ID)
		}
		fragment := ""
		if callDelta.Function != nil {
			if callDelta.Function.Name != nil {
				name := *callDelta.Function.Name
				block.name = &name
			}
			if callDelta.Function.Arguments != nil {
				fragment = *callDelta.Function.Arguments
			}
		}
		block.text += fragment
		streamState.queue = append(streamState.queue, llm.ToolCallDeltaChunk{
			Type: "tool-call-delta", Index: block.index, ID: block.callID,
			Name: cloneString(block.name), ArgumentsDelta: fragment,
		})
	}
}

func (streamState *translatedStream) open(kind string) *openBlock {
	block := &openBlock{index: streamState.nextIndex, kind: kind}
	streamState.nextIndex++
	streamState.order = append(streamState.order, block)
	return block
}

func closeTranslatedBlock(block *openBlock) llm.ContentBlock {
	switch block.kind {
	case "text":
		return llm.TextBlock{Type: "text", Text: block.text}
	case "reasoning":
		return llm.ReasoningBlock{Type: "reasoning", Text: block.text}
	default:
		name := ""
		if block.name != nil {
			name = *block.name
		}
		return llm.ToolCallBlock{Type: "tool-call", ID: block.callID, Name: name, Arguments: block.text}
	}
}

// MapFinishReason normalizes DeepSeek's finish_reason vocabulary.
func MapFinishReason(reason string) llm.FinishReason {
	switch reason {
	case "stop":
		return llm.StopFinish{Kind: "stop"}
	case "tool_calls":
		return llm.ToolCallsFinish{Kind: "tool-calls"}
	case "length":
		return llm.MaxTokensFinish{Kind: "max-tokens"}
	default:
		return llm.ErrorFinish{Kind: "error", Failure: llm.LlmFailure{
			Message: "model stopped: " + reason,
			Code:    strings.ToUpper(reason),
		}}
	}
}

// MapUsage converts cumulative provider input accounting to disjoint Harness counts.
func MapUsage(wireAccounting wireUsage) llm.TokenUsage {
	cacheRead := wireAccounting.PromptCacheHitTokens
	if wireAccounting.PromptTokensDetails != nil && wireAccounting.PromptTokensDetails.CachedTokens != nil {
		cacheRead = wireAccounting.PromptTokensDetails.CachedTokens
	}
	result := llm.TokenUsage{
		InputTokens:  wireAccounting.PromptTokens,
		OutputTokens: wireAccounting.CompletionTokens,
	}
	if cacheRead != nil {
		result.InputTokens -= *cacheRead
		value := *cacheRead
		result.CacheReadTokens = &value
	}
	if wireAccounting.CompletionTokensDetails != nil && wireAccounting.CompletionTokensDetails.ReasoningTokens != nil {
		value := *wireAccounting.CompletionTokensDetails.ReasoningTokens
		result.ReasoningTokens = &value
	}
	return result
}

func truncatePayload(payload string, maximumRunes int) string {
	runes := []rune(payload)
	if len(runes) <= maximumRunes {
		return payload
	}
	return string(runes[:maximumRunes])
}

func (streamState *translatedStream) Close(closeContext context.Context) error {
	streamState.mu.Lock()
	streamState.closed = true
	streamState.queue = nil
	streamState.mu.Unlock()
	return streamState.upstream.Close(closeContext)
}
