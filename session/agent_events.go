package session

import (
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/llm"
)

const (
	TurnStartEventName        = "turn/start"
	TurnEndEventName          = "turn/end"
	StepStartEventName        = "step/start"
	StepEndEventName          = "step/end"
	UserMessageEventName      = "user/message"
	AssistantChunkEventName   = "assistant/chunk"
	AssistantMessageEventName = "assistant/message"
	ToolCallEventName         = "tool/call"
	ToolResultEventName       = "tool/result"
	RequestHeaderEventName    = "request/header"
	RequestContextEventName   = "request/context"
)

// TurnStart opens one durable Agent turn.
type TurnStart struct {
	Turn int64 `json:"turn"`
}

// StepPosition identifies one model step inside a turn.
type StepPosition struct {
	Turn int64 `json:"turn"`
	Step int64 `json:"step"`
}

// TurnCancelCause is the durable cancellation vocabulary stored in turn/end.
type TurnCancelCause interface {
	CancelKind() string
}

type UserCancelCause struct{}

func (UserCancelCause) CancelKind() string { return "user" }
func (UserCancelCause) MarshalJSON() ([]byte, error) {
	return []byte(`{"kind":"user"}`), nil
}

type ParentCancelCause struct{}

func (ParentCancelCause) CancelKind() string { return "parent" }
func (ParentCancelCause) MarshalJSON() ([]byte, error) {
	return []byte(`{"kind":"parent"}`), nil
}

type DisposedCancelCause struct{}

func (DisposedCancelCause) CancelKind() string { return "disposed" }
func (DisposedCancelCause) MarshalJSON() ([]byte, error) {
	return []byte(`{"kind":"disposed"}`), nil
}

// HookCancelCause retains one extension-owned cancellation reason.
type HookCancelCause struct {
	Reason string `json:"reason"`
}

func (HookCancelCause) CancelKind() string { return "hook" }
func (cause HookCancelCause) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
	}{
		Kind:   "hook",
		Reason: cause.Reason,
	})
}

// TurnEndReason is the closed core reason union stored by Agent Loop.
type TurnEndReason interface {
	TurnEndKind() string
}

type TurnCompleted struct{}

func (TurnCompleted) TurnEndKind() string { return "completed" }
func (TurnCompleted) MarshalJSON() ([]byte, error) {
	return []byte(`{"kind":"completed"}`), nil
}

type TurnBlocked struct{}

func (TurnBlocked) TurnEndKind() string { return "blocked" }
func (TurnBlocked) MarshalJSON() ([]byte, error) {
	return []byte(`{"kind":"blocked"}`), nil
}

type TurnMaxTokens struct{}

func (TurnMaxTokens) TurnEndKind() string { return "max-tokens" }
func (TurnMaxTokens) MarshalJSON() ([]byte, error) {
	return []byte(`{"kind":"max-tokens"}`), nil
}

type TurnInterrupted struct{}

func (TurnInterrupted) TurnEndKind() string { return "interrupted" }
func (TurnInterrupted) MarshalJSON() ([]byte, error) {
	return []byte(`{"kind":"interrupted"}`), nil
}

// TurnAborted records a live cancellation and its stable caller intent.
type TurnAborted struct {
	Reason TurnCancelCause `json:"reason"`
}

func (TurnAborted) TurnEndKind() string { return "aborted" }
func (outcome TurnAborted) MarshalJSON() ([]byte, error) {
	if outcome.Reason == nil {
		return nil, errors.New("session: aborted turn needs a cancellation reason")
	}
	return json.Marshal(struct {
		Kind   string          `json:"kind"`
		Reason TurnCancelCause `json:"reason"`
	}{
		Kind:   "aborted",
		Reason: outcome.Reason,
	})
}

// TurnError records one structured provider-neutral failure.
type TurnError struct {
	Error llm.LlmFailure `json:"error"`
}

func (TurnError) TurnEndKind() string { return "error" }
func (outcome TurnError) MarshalJSON() ([]byte, error) {
	if outcome.Error.Message == "" || outcome.Error.Code == "" {
		return nil, errors.New("session: failed turn needs a structured error")
	}
	return json.Marshal(struct {
		Kind  string         `json:"kind"`
		Error llm.LlmFailure `json:"error"`
	}{
		Kind:  "error",
		Error: outcome.Error,
	})
}

// TurnEnd closes one durable Agent turn.
type TurnEnd struct {
	Turn   int64         `json:"turn"`
	Reason TurnEndReason `json:"reason"`
}

// AssistantChunk records one raw provider-neutral stream item.
type AssistantChunk struct {
	Turn  int64           `json:"turn"`
	Step  int64           `json:"step"`
	Chunk llm.StreamChunk `json:"chunk"`
}

// AssistantMessage records one assembled model response and optional usage.
type AssistantMessage struct {
	Turn    int64                `json:"turn"`
	Step    int64                `json:"step"`
	Message llm.AssistantMessage `json:"message"`
	Usage   *llm.TokenUsage      `json:"usage,omitempty"`
}

// ToolCall records the exact raw model arguments before host execution.
type ToolCall struct {
	Turn      int64      `json:"turn"`
	Step      int64      `json:"step"`
	CallID    llm.CallID `json:"callId"`
	Name      string     `json:"name"`
	Arguments string     `json:"arguments"`
}

// ToolErrorInfo is stable internal failure-routing metadata.
type ToolErrorInfo struct {
	Name string `json:"name"`
	Code string `json:"code"`
}

// ToolResult records one model-facing result with optional host metadata.
type ToolResult struct {
	Turn    int64                 `json:"turn"`
	Step    int64                 `json:"step"`
	Message llm.ToolResultMessage `json:"message"`
	Error   *ToolErrorInfo        `json:"error,omitempty"`
	Meta    json.RawMessage       `json:"meta,omitempty"`
}

// EpochHeader is the complete non-message state for one reconstructed request.
type EpochHeader struct {
	Config          llm.CallConfig                 `json:"config"`
	AdapterDefaults *llm.CallConfigAdapterDefaults `json:"adapterDefaults,omitempty"`
	System          *string                        `json:"system,omitempty"`
	Tools           []llm.ToolSchema               `json:"tools,omitempty"`
}

// RequestHeaderReason classifies why a full request header was appended.
type RequestHeaderReason string

const (
	RequestHeaderInitial RequestHeaderReason = "initial"
	RequestHeaderResume  RequestHeaderReason = "resume"
	RequestHeaderChange  RequestHeaderReason = "change"
)

// RequestHeaderSnapshot updates the request reconstruction fold.
type RequestHeaderSnapshot struct {
	Header EpochHeader         `json:"header"`
	Reason RequestHeaderReason `json:"reason"`
}

// RequestRouteContext records resolved model-route capacity metadata.
type RequestRouteContext struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	ContextWindow *int   `json:"contextWindow,omitempty"`
}

var (
	TurnStarted       = DefineEvent[TurnStart](TurnStartEventName)
	TurnEnded         = DefineEvent[TurnEnd](TurnEndEventName)
	StepStarted       = DefineEvent[StepPosition](StepStartEventName)
	StepEnded         = DefineEvent[StepPosition](StepEndEventName)
	UserMessageAdded  = defineSurfaceEvent[llm.UserMessage](UserMessageEventName)
	AssistantChunked  = DefineEvent[AssistantChunk](AssistantChunkEventName)
	AssistantMessaged = defineSurfaceEvent[AssistantMessage](AssistantMessageEventName)
	ToolCalled        = DefineEvent[ToolCall](ToolCallEventName)
	ToolResultAdded   = defineSurfaceEvent[ToolResult](ToolResultEventName)
	RequestHeaderSet  = DefineEvent[RequestHeaderSnapshot](RequestHeaderEventName)
	RequestContextSet = DefineEvent[RequestRouteContext](RequestContextEventName)
)
