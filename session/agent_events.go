package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

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
	}{Kind: "hook", Reason: cause.Reason})
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
	}{Kind: "aborted", Reason: outcome.Reason})
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
	}{Kind: "error", Error: outcome.Error})
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

// CanonicalEpochHeader detaches a request header and removes empty optional fields.
func CanonicalEpochHeader(inputSnapshot EpochHeader) EpochHeader {
	canonical := EpochHeader{Config: llm.CloneCallConfig(inputSnapshot.Config)}
	if inputSnapshot.AdapterDefaults != nil &&
		(inputSnapshot.AdapterDefaults.ReasoningEffort || inputSnapshot.AdapterDefaults.MaxTokens) {
		defaultsSnapshot := *inputSnapshot.AdapterDefaults
		canonical.AdapterDefaults = &defaultsSnapshot
	}
	if inputSnapshot.System != nil && *inputSnapshot.System != "" {
		promptSnapshot := *inputSnapshot.System
		canonical.System = &promptSnapshot
	}
	if len(inputSnapshot.Tools) != 0 {
		canonical.Tools = cloneToolSchemas(inputSnapshot.Tools)
	}
	return canonical
}

// EpochHeaderEqual compares canonical request headers, including ordered schemas.
func EpochHeaderEqual(left EpochHeader, right EpochHeader) bool {
	leftCanonical := CanonicalEpochHeader(left)
	rightCanonical := CanonicalEpochHeader(right)
	if !llm.CallConfigEqual(leftCanonical.Config, rightCanonical.Config) ||
		!sameAdapterDefaults(leftCanonical.AdapterDefaults, rightCanonical.AdapterDefaults) ||
		!sameOptionalString(leftCanonical.System, rightCanonical.System) || len(leftCanonical.Tools) != len(rightCanonical.Tools) {
		return false
	}
	for index := range leftCanonical.Tools {
		leftSchema := leftCanonical.Tools[index]
		rightSchema := rightCanonical.Tools[index]
		if leftSchema.Name != rightSchema.Name || leftSchema.Description != rightSchema.Description ||
			!bytes.Equal(leftSchema.Parameters, rightSchema.Parameters) {
			return false
		}
	}
	return true
}

func cloneToolSchemas(entries []llm.ToolSchema) []llm.ToolSchema {
	detached := make([]llm.ToolSchema, len(entries))
	for index, entry := range entries {
		detached[index] = entry
		detached[index].Parameters = append(json.RawMessage(nil), entry.Parameters...)
	}
	return detached
}

func sameAdapterDefaults(left *llm.CallConfigAdapterDefaults, right *llm.CallConfigAdapterDefaults) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameOptionalString(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func decodeSessionPayload[T any](rawValue json.RawMessage, target *T) error {
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("session: event payload contains multiple JSON values")
		}
		return err
	}
	return nil
}

func cloneInt(source *int) *int {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}

func decodeDerivedMessage(entry Event) (llm.Message, bool, error) {
	switch entry.Type {
	case UserMessageEventName:
		messageValue, err := llm.DecodeUserMessage(entry.Data)
		return messageValue, err == nil, err
	case AssistantMessageEventName:
		var wireValue struct {
			Turn    int64           `json:"turn"`
			Step    int64           `json:"step"`
			Message json.RawMessage `json:"message"`
			Usage   json.RawMessage `json:"usage,omitempty"`
		}
		if err := decodeSessionPayload(entry.Data, &wireValue); err != nil {
			return nil, false, err
		}
		messageValue, err := llm.DecodeMessage(wireValue.Message)
		if err != nil {
			return nil, false, err
		}
		typedMessage, ok := messageValue.(llm.AssistantMessage)
		if !ok {
			return nil, false, errors.New("session: assistant/message contains a non-assistant message")
		}
		if len(typedMessage.ContentValue()) == 0 {
			return nil, false, nil
		}
		return typedMessage, true, nil
	case ToolResultEventName:
		var wireValue struct {
			Turn    int64           `json:"turn"`
			Step    int64           `json:"step"`
			Message json.RawMessage `json:"message"`
			Error   json.RawMessage `json:"error,omitempty"`
			Meta    json.RawMessage `json:"meta,omitempty"`
		}
		if err := decodeSessionPayload(entry.Data, &wireValue); err != nil {
			return nil, false, err
		}
		messageValue, err := llm.DecodeMessage(wireValue.Message)
		if err != nil {
			return nil, false, err
		}
		typedMessage, ok := messageValue.(llm.ToolResultMessage)
		if !ok {
			return nil, false, errors.New("session: tool/result contains a non-tool-result message")
		}
		return typedMessage, true, nil
	default:
		return nil, false, nil
	}
}

func validatePosition(turn int64, step int64) error {
	if !isSafeNonNegative(turn) || turn == 0 {
		return fmt.Errorf("session: turn must be a positive safe integer, got %d", turn)
	}
	if !isSafeNonNegative(step) || step == 0 {
		return fmt.Errorf("session: step must be a positive safe integer, got %d", step)
	}
	return nil
}
