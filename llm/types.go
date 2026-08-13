package llm

import (
	"context"
	"encoding/json"
	"time"
)

// API identifies an LLM wire protocol and therefore selects an adapter.
type API string

// Provider identifies who serves a model. Several providers can share one API.
type Provider string

// InputModality identifies an input kind accepted by a model.
type InputModality string

const (
	// InputText declares text input support.
	InputText InputModality = "text"
	// InputImage declares image input support.
	InputImage InputModality = "image"
)

// Model describes routing, capabilities, limits, and pricing for one model.
type Model struct {
	ID              string
	Name            string
	API             API
	Provider        Provider
	BaseURL         string
	Reasoning       bool
	ReasoningLevels []ReasoningLevel
	ReasoningMap    map[ReasoningLevel]string
	ReasoningBudget map[ReasoningLevel]int
	Input           []InputModality
	ContextWindow   int
	MaxOutputTokens int
	Headers         map[string]string
	Cost            CostRates
	ServiceTierCost map[string]float64
}

// CostRates are USD prices per million tokens.
type CostRates struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// Role identifies the semantic role of a conversation message.
type Role string

const (
	// RoleUser identifies a user message.
	RoleUser Role = "user"
	// RoleAssistant identifies an assistant message.
	RoleAssistant Role = "assistant"
	// RoleToolResult identifies the result of an assistant tool call.
	RoleToolResult Role = "toolResult"
)

// Message is a closed set of provider-neutral conversation messages.
type Message interface {
	Role() Role
	isMessage()
}

// UserMessage contains text or image input from the user.
type UserMessage struct {
	Content   []UserContent
	Timestamp time.Time
}

// Role returns RoleUser.
func (UserMessage) Role() Role { return RoleUser }
func (UserMessage) isMessage() {}

// AssistantMessage is the normalized result of an LLM invocation. Runtime
// failure is represented by StopReasonError or StopReasonAborted and ErrorMessage.
type AssistantMessage struct {
	Content       []AssistantContent
	API           API
	Provider      Provider
	Model         string
	ResponseModel string
	ResponseID    string
	Usage         Usage
	StopReason    StopReason
	ErrorMessage  string
	Timestamp     time.Time
}

// Role returns RoleAssistant.
func (AssistantMessage) Role() Role { return RoleAssistant }
func (AssistantMessage) isMessage() {}

// ToolResultMessage returns one tool call's result to the model.
type ToolResultMessage struct {
	ToolCallID string
	ToolName   string
	Content    []ToolResultContent
	IsError    bool
	Timestamp  time.Time
}

// Role returns RoleToolResult.
func (ToolResultMessage) Role() Role { return RoleToolResult }
func (ToolResultMessage) isMessage() {}

// UserContent is a closed set of content accepted in user messages.
type UserContent interface {
	isUserContent()
}

// AssistantContent is a closed set of content emitted by an assistant.
type AssistantContent interface {
	isAssistantContent()
}

// ToolResultContent is a closed set of content returned by a tool.
type ToolResultContent interface {
	isToolResultContent()
}

// TextContent contains visible text.
type TextContent struct {
	Text string
}

func (TextContent) isUserContent()       {}
func (TextContent) isToolResultContent() {}

// AssistantTextPhase identifies the user-visible phase of assistant text. It
// is content metadata, not an Agent state or a reasoning marker.
type AssistantTextPhase string

const (
	// AssistantTextPhaseUnspecified means the protocol did not expose a phase.
	AssistantTextPhaseUnspecified AssistantTextPhase = "unspecified"
	// AssistantTextPhaseCommentary identifies intermediate user-visible text.
	AssistantTextPhaseCommentary AssistantTextPhase = "commentary"
	// AssistantTextPhaseFinalAnswer identifies final user-visible text.
	AssistantTextPhaseFinalAnswer AssistantTextPhase = "final_answer"
)

// ReplayMetadata stores protocol-private replay data together with the exact
// model identity for which that data is valid. Data remains opaque to llm.
type ReplayMetadata struct {
	API      API             `json:"api"`
	Provider Provider        `json:"provider"`
	Model    string          `json:"model"`
	Data     json.RawMessage `json:"data"`
}

// AssistantTextContent contains visible assistant text and optional replay
// metadata. User and tool-result text intentionally use TextContent instead.
type AssistantTextContent struct {
	Text     string
	Phase    AssistantTextPhase
	Metadata *ReplayMetadata
}

func (AssistantTextContent) isAssistantContent() {}

// ImageContent contains base64-encoded image data and its MIME type.
type ImageContent struct {
	Data     string
	MIMEType string
}

func (ImageContent) isUserContent()       {}
func (ImageContent) isToolResultContent() {}

// ThinkingContent contains provider-supplied reasoning and replay metadata.
type ThinkingContent struct {
	Thinking  string
	Signature string
	Redacted  bool
	Metadata  *ReplayMetadata
}

func (ThinkingContent) isAssistantContent() {}

// ToolCall requests invocation of a named tool with JSON arguments.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
	Metadata  *ReplayMetadata
}

func (ToolCall) isAssistantContent() {}

// Tool describes a callable function and its JSON Schema parameters.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict,omitempty"`
	validator   toolArgumentsValidator
	validated   string
}

// Context is the complete provider-neutral input to one model invocation.
type Context struct {
	SystemPrompt string
	Messages     []Message
	Tools        []Tool
}

// ReasoningLevel requests a provider-supported reasoning effort.
type ReasoningLevel string

const (
	// ReasoningOff disables reasoning when the target API supports it.
	ReasoningOff ReasoningLevel = "off"
	// ReasoningMinimal requests minimal reasoning effort.
	ReasoningMinimal ReasoningLevel = "minimal"
	// ReasoningLow requests low reasoning effort.
	ReasoningLow ReasoningLevel = "low"
	// ReasoningMedium requests medium reasoning effort.
	ReasoningMedium ReasoningLevel = "medium"
	// ReasoningHigh requests high reasoning effort.
	ReasoningHigh ReasoningLevel = "high"
	// ReasoningXHigh requests extra-high reasoning effort.
	ReasoningXHigh ReasoningLevel = "xhigh"
	// ReasoningMax requests the maximum supported reasoning effort.
	ReasoningMax ReasoningLevel = "max"
)

// ReasoningSummary controls whether a provider emits a reasoning summary.
type ReasoningSummary string

const (
	// ReasoningSummaryAuto lets the provider choose summary behavior.
	ReasoningSummaryAuto ReasoningSummary = "auto"
	// ReasoningSummaryConcise requests a concise summary.
	ReasoningSummaryConcise ReasoningSummary = "concise"
	// ReasoningSummaryDetailed requests a detailed summary.
	ReasoningSummaryDetailed ReasoningSummary = "detailed"
)

// ToolChoiceMode controls function-tool selection.
type ToolChoiceMode string

const (
	// ToolChoiceAuto lets the model choose between text and tools.
	ToolChoiceAuto ToolChoiceMode = "auto"
	// ToolChoiceNone prevents tool calls.
	ToolChoiceNone ToolChoiceMode = "none"
	// ToolChoiceRequired requires at least one tool call.
	ToolChoiceRequired ToolChoiceMode = "required"
	// ToolChoiceFunction requires one named function tool.
	ToolChoiceFunction ToolChoiceMode = "function"
)

// ToolChoice is a provider-neutral function-tool selection request.
type ToolChoice struct {
	Mode ToolChoiceMode
	Name string
}

// CacheRetention requests a provider-supported prompt-cache lifetime.
type CacheRetention string

const (
	// CacheRetentionMemory requests the provider's in-memory lifetime.
	CacheRetentionMemory CacheRetention = "in_memory"
	// CacheRetention24Hours requests an extended 24-hour lifetime.
	CacheRetention24Hours CacheRetention = "24h"
)

// RequestInfo is the safe, provider-neutral request metadata exposed to hooks.
// It contains neither credentials nor the prompt body.
type RequestInfo struct {
	API       API
	Provider  Provider
	Model     string
	RequestID string
	Metadata  map[string]string
}

// ResponseInfo is the safe HTTP response metadata exposed to hooks.
type ResponseInfo struct {
	RequestID  string
	StatusCode int
	Headers    map[string][]string
}

// RequestHook observes a request immediately before transport execution.
type RequestHook func(context.Context, RequestInfo) error

// ResponseHook observes response metadata without receiving the response body.
type ResponseHook func(context.Context, ResponseInfo) error

// RequestPayload exposes a credential-free JSON request body to a hook. Body is
// an inspection-only copy. Set replaces top-level or dot-separated fields in
// the request sent to the provider.
type RequestPayload struct {
	Body json.RawMessage
	Set  map[string]any
}

// RequestTransform can inspect the request and add or replace provider fields
// through Set. Body must be returned unchanged and credentials are never included.
type RequestTransform func(context.Context, RequestInfo, RequestPayload) (RequestPayload, error)

// JSONSchemaFormat requests a provider-native structured JSON response.
type JSONSchemaFormat struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Strict      bool
}

// StreamOptions controls one LLM invocation without changing model identity.
type StreamOptions struct {
	APIKey           string
	Temperature      *float64
	MaxOutputTokens  int
	Reasoning        ReasoningLevel
	ReasoningSummary ReasoningSummary
	ThinkingBudget   int
	Headers          map[string]string
	ResponseFormat   *JSONSchemaFormat
	ToolChoice       *ToolChoice
	Timeout          time.Duration
	MaxRetries       *int
	MaxRetryDelay    time.Duration
	CacheKey         string
	CacheRetention   CacheRetention
	SessionID        string
	RequestID        string
	Metadata         map[string]string
	ServiceTier      string
	BeforeRequest    RequestHook
	TransformRequest RequestTransform
	AfterResponse    ResponseHook
}

// Clone returns an isolated invocation snapshot suitable for asynchronous
// adapter execution. Function hooks are immutable values and are copied as-is.
func (invocationOptions StreamOptions) Clone() StreamOptions {
	cloned := invocationOptions
	if invocationOptions.Temperature != nil {
		value := *invocationOptions.Temperature
		cloned.Temperature = &value
	}
	if invocationOptions.MaxRetries != nil {
		value := *invocationOptions.MaxRetries
		cloned.MaxRetries = &value
	}
	if invocationOptions.Headers != nil {
		cloned.Headers = make(map[string]string, len(invocationOptions.Headers))
		for name, value := range invocationOptions.Headers {
			cloned.Headers[name] = value
		}
	}
	if invocationOptions.Metadata != nil {
		cloned.Metadata = make(map[string]string, len(invocationOptions.Metadata))
		for name, value := range invocationOptions.Metadata {
			cloned.Metadata[name] = value
		}
	}
	if invocationOptions.ResponseFormat != nil {
		responseFormat := *invocationOptions.ResponseFormat
		responseFormat.Schema = append(json.RawMessage(nil), invocationOptions.ResponseFormat.Schema...)
		cloned.ResponseFormat = &responseFormat
	}
	if invocationOptions.ToolChoice != nil {
		toolSelection := *invocationOptions.ToolChoice
		cloned.ToolChoice = &toolSelection
	}
	return cloned
}

// Usage is normalized token use and calculated cost for a response.
type Usage struct {
	InputTokens      int    `json:"input_tokens,omitempty"`
	OutputTokens     int    `json:"output_tokens,omitempty"`
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int    `json:"cache_write_tokens,omitempty"`
	TotalTokens      int    `json:"total_tokens,omitempty"`
	ServiceTier      string `json:"service_tier,omitempty"`
	Cost             Cost   `json:"cost,omitempty"`
}

// Cost is the USD cost attributed to normalized token categories.
type Cost struct {
	Input      float64 `json:"input,omitempty"`
	Output     float64 `json:"output,omitempty"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
	Total      float64 `json:"total,omitempty"`
}

// CalculateCost applies the model's per-million-token rates to usage.
func (m Model) CalculateCost(tokenUsage Usage) Cost {
	return m.CalculateCostForTier(tokenUsage, tokenUsage.ServiceTier)
}

// CalculateCostForTier applies token rates and an optional service-tier
// multiplier. Missing tiers use multiplier 1.
func (m Model) CalculateCostForTier(tokenUsage Usage, serviceTier string) Cost {
	multiplier := 1.0
	if configured := m.ServiceTierCost[serviceTier]; configured > 0 {
		multiplier = configured
	}
	calculatedCost := Cost{
		Input:      multiplier * m.Cost.Input * float64(tokenUsage.InputTokens) / 1_000_000,
		Output:     multiplier * m.Cost.Output * float64(tokenUsage.OutputTokens) / 1_000_000,
		CacheRead:  multiplier * m.Cost.CacheRead * float64(tokenUsage.CacheReadTokens) / 1_000_000,
		CacheWrite: multiplier * m.Cost.CacheWrite * float64(tokenUsage.CacheWriteTokens) / 1_000_000,
	}
	calculatedCost.Total = calculatedCost.Input + calculatedCost.Output + calculatedCost.CacheRead + calculatedCost.CacheWrite
	return calculatedCost
}

// StopReason describes why an assistant stream terminated.
type StopReason string

const (
	// StopReasonStop indicates normal completion.
	StopReasonStop StopReason = "stop"
	// StopReasonLength indicates the output token limit was reached.
	StopReasonLength StopReason = "length"
	// StopReasonToolUse indicates that the model requested one or more tools.
	StopReasonToolUse StopReason = "toolUse"
	// StopReasonError indicates provider or runtime failure.
	StopReasonError StopReason = "error"
	// StopReasonAborted indicates context cancellation.
	StopReasonAborted StopReason = "aborted"
)

// NewTextMessage constructs a timestamped user text message.
func NewTextMessage(messageText string) UserMessage {
	return UserMessage{
		Content:   []UserContent{TextContent{Text: messageText}},
		Timestamp: time.Now(),
	}
}

// Text concatenates all visible text blocks in an assistant message.
func Text(assistantReply AssistantMessage) string {
	var visibleText string
	for _, part := range assistantReply.Content {
		if block, ok := part.(AssistantTextContent); ok {
			visibleText += block.Text
		}
	}
	return visibleText
}
