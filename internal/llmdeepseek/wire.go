package llmdeepseek

import "encoding/json"

type wireMessage interface {
	wireRole() string
}

type wireSystemMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (wireSystemMessage) wireRole() string { return "system" }

type wireUserMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (wireUserMessage) wireRole() string { return "user" }

type wireToolMessage struct {
	Role       string `json:"role"`
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
}

func (wireToolMessage) wireRole() string { return "tool" }

type wireAssistantMessage struct {
	Role             string         `json:"role"`
	Content          *string        `json:"content"`
	ReasoningContent *string        `json:"reasoning_content,omitempty"`
	ToolCalls        []wireToolCall `json:"tool_calls,omitempty"`
}

func (wireAssistantMessage) wireRole() string { return "assistant" }

type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireToolFunction `json:"function"`
}

type wireToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireTool struct {
	Type     string             `json:"type"`
	Function wireToolDefinition `json:"function"`
}

type wireToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type wireThinking struct {
	Type ThinkingMode `json:"type"`
}

type wireRequest struct {
	Model           string            `json:"model"`
	Messages        []wireMessage     `json:"messages"`
	Stream          bool              `json:"stream"`
	StreamOptions   wireStreamOptions `json:"stream_options"`
	Thinking        *wireThinking     `json:"thinking,omitempty"`
	ReasoningEffort *ReasoningEffort  `json:"reasoning_effort,omitempty"`
	Tools           []wireTool        `json:"tools,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	MaxTokens       *int              `json:"max_tokens,omitempty"`
	Stop            *[]string         `json:"stop,omitempty"`
}

type wireStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireChunk struct {
	Choices []*wireChoice `json:"choices,omitempty"`
	Usage   *wireUsage    `json:"usage,omitempty"`
}

type wireChoice struct {
	Delta        *wireDelta `json:"delta,omitempty"`
	FinishReason *string    `json:"finish_reason,omitempty"`
}

type wireDelta struct {
	Role             *string             `json:"role,omitempty"`
	Content          *string             `json:"content,omitempty"`
	ReasoningContent *string             `json:"reasoning_content,omitempty"`
	ToolCalls        []wireToolCallDelta `json:"tool_calls,omitempty"`
}

type wireToolCallDelta struct {
	Index    int                `json:"index"`
	ID       *string            `json:"id,omitempty"`
	Type     *string            `json:"type,omitempty"`
	Function *wireFunctionDelta `json:"function,omitempty"`
}

type wireFunctionDelta struct {
	Name      *string `json:"name,omitempty"`
	Arguments *string `json:"arguments,omitempty"`
}

type wireUsage struct {
	PromptTokens            int64                   `json:"prompt_tokens"`
	CompletionTokens        int64                   `json:"completion_tokens"`
	PromptCacheHitTokens    *int64                  `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens   *int64                  `json:"prompt_cache_miss_tokens,omitempty"`
	PromptTokensDetails     *wirePromptTokenDetails `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *wireCompletionDetails  `json:"completion_tokens_details,omitempty"`
}

type wirePromptTokenDetails struct {
	CachedTokens *int64 `json:"cached_tokens,omitempty"`
}

type wireCompletionDetails struct {
	ReasoningTokens *int64 `json:"reasoning_tokens,omitempty"`
}

type wireErrorBody struct {
	Error *wireErrorDetail `json:"error,omitempty"`
}

type wireErrorDetail struct {
	Message string `json:"message,omitempty"`
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
}
