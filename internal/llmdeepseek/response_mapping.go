package llmdeepseek

import (
	"strings"

	"github.com/gorenx/goren/llm"
)

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
