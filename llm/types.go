package llm

import (
	"encoding/json"
	"time"
)

// API identifies an LLM wire protocol and therefore selects an adapter.
type API string

// Provider identifies who serves a model. Several providers can share one API.
type Provider string

// APIOpenAICompletions identifies the OpenAI-compatible Chat Completions wire protocol.
const APIOpenAICompletions API = "openai-completions"

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
	Input           []InputModality
	ContextWindow   int
	MaxOutputTokens int
	Headers         map[string]string
	Cost            CostRates
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
func (TextContent) isAssistantContent()  {}
func (TextContent) isToolResultContent() {}

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
}

func (ThinkingContent) isAssistantContent() {}

// ToolCall requests invocation of a named tool with JSON arguments.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

func (ToolCall) isAssistantContent() {}

// Tool describes a callable function and its JSON Schema parameters.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Strict      bool
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
)

// JSONSchemaFormat requests a provider-native structured JSON response.
type JSONSchemaFormat struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Strict      bool
}

// StreamOptions controls one LLM invocation without changing model identity.
type StreamOptions struct {
	APIKey          string
	Temperature     *float64
	MaxOutputTokens int
	Reasoning       ReasoningLevel
	Headers         map[string]string
	ResponseFormat  *JSONSchemaFormat
}

// Usage is normalized token use and calculated cost for a response.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	TotalTokens      int
	Cost             Cost
}

// Cost is the USD cost attributed to normalized token categories.
type Cost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
	Total      float64
}

// CalculateCost applies the model's per-million-token rates to usage.
func (m Model) CalculateCost(tokenUsage Usage) Cost {
	calculatedCost := Cost{
		Input:      m.Cost.Input * float64(tokenUsage.InputTokens) / 1_000_000,
		Output:     m.Cost.Output * float64(tokenUsage.OutputTokens) / 1_000_000,
		CacheRead:  m.Cost.CacheRead * float64(tokenUsage.CacheReadTokens) / 1_000_000,
		CacheWrite: m.Cost.CacheWrite * float64(tokenUsage.CacheWriteTokens) / 1_000_000,
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
		if block, ok := part.(TextContent); ok {
			visibleText += block.Text
		}
	}
	return visibleText
}
