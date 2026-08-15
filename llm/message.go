package llm

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf16"

	"github.com/gorenx/goren/internal/jsonvalue"
)

// MessageRole is the provider-neutral conversation role.
type MessageRole string

const (
	// RoleSystem identifies system-role context.
	RoleSystem MessageRole = "system"
	// RoleUser identifies user input and tool results.
	RoleUser MessageRole = "user"
	// RoleAssistant identifies model or plugin assistant output.
	RoleAssistant MessageRole = "assistant"
)

// ContextForm describes the semantic form of producer-supplied context.
type ContextForm string

const (
	ContextInstructions ContextForm = "instructions"
	ContextCatalog      ContextForm = "catalog"
	ContextSnapshot     ContextForm = "snapshot"
	ContextNotice       ContextForm = "notice"
	ContextRelay        ContextForm = "relay"
	ContextRecall       ContextForm = "recall"
)

// ContextSummaryMaxChars is the source-compatible notice-summary bound.
const ContextSummaryMaxChars = 120

// BoundContextSummary applies the transcript notice bound using UTF-16 code
// units, matching JavaScript string length for normal Unicode text.
func BoundContextSummary(summary string) string {
	units := utf16.Encode([]rune(summary))
	if len(units) <= ContextSummaryMaxChars {
		return summary
	}
	return string(utf16.Decode(units[:ContextSummaryMaxChars-1])) + "…"
}

// MessageSource is the merge-extensible producer-provenance contract.
type MessageSource interface {
	SourceKind() string
	CloneSource() (MessageSource, error)
}

// UserMessageSource identifies direct user input.
type UserMessageSource struct {
	Kind string `json:"kind"`
}

func (UserMessageSource) SourceKind() string { return "user" }

func (origin UserMessageSource) CloneSource() (MessageSource, error) {
	origin.Kind = "user"
	return origin, nil
}

// PluginMessageSource attributes context to a plugin and its semantic form.
type PluginMessageSource struct {
	Kind     string                   `json:"kind"`
	Plugin   string                   `json:"plugin"`
	Form     ContextForm              `json:"form,omitempty"`
	Sections []ContextSnapshotSection `json:"sections,omitempty"`
	Summary  string                   `json:"summary,omitempty"`
}

func (PluginMessageSource) SourceKind() string { return "plugin" }

func (origin PluginMessageSource) CloneSource() (MessageSource, error) {
	if origin.Plugin == "" {
		return nil, errors.New("llm: plugin message source needs a plugin name")
	}
	if err := validateContextForm(origin); err != nil {
		return nil, err
	}
	origin.Kind = "plugin"
	origin.Sections = append([]ContextSnapshotSection(nil), origin.Sections...)
	return origin, nil
}

// MarshalJSON keeps form-required fields present even when their values are empty.
func (origin PluginMessageSource) MarshalJSON() ([]byte, error) {
	detachedOrigin, err := origin.CloneSource()
	if err != nil {
		return nil, err
	}
	validated := detachedOrigin.(PluginMessageSource)
	wireValue := struct {
		Kind     string                    `json:"kind"`
		Plugin   string                    `json:"plugin"`
		Form     ContextForm               `json:"form,omitempty"`
		Sections *[]ContextSnapshotSection `json:"sections,omitempty"`
		Summary  *string                   `json:"summary,omitempty"`
	}{Kind: "plugin", Plugin: validated.Plugin, Form: validated.Form}
	if validated.Form == ContextSnapshot {
		sectionsCopy := append([]ContextSnapshotSection(nil), validated.Sections...)
		if sectionsCopy == nil {
			sectionsCopy = []ContextSnapshotSection{}
		}
		wireValue.Sections = &sectionsCopy
	}
	if validated.Form == ContextNotice {
		summaryCopy := validated.Summary
		wireValue.Summary = &summaryCopy
	}
	return json.Marshal(wireValue)
}

func validateContextForm(origin PluginMessageSource) error {
	switch origin.Form {
	case "", ContextInstructions, ContextCatalog, ContextRelay, ContextRecall:
		if len(origin.Sections) != 0 || origin.Summary != "" {
			return errors.New("llm: plugin context fields do not match its form")
		}
	case ContextSnapshot:
		if origin.Sections == nil || origin.Summary != "" {
			return errors.New("llm: snapshot context needs sections and no summary")
		}
	case ContextNotice:
		if len(origin.Sections) != 0 {
			return errors.New("llm: notice context cannot contain sections")
		}
	default:
		return fmt.Errorf("llm: unsupported context form %q", origin.Form)
	}
	return nil
}

// ModelMessageSource retains provider/model identity and optional replay state.
type ModelMessageSource struct {
	Kind        string          `json:"kind"`
	Provider    string          `json:"provider"`
	Model       string          `json:"model"`
	ReplayState json.RawMessage `json:"replayState,omitempty"`
}

func (ModelMessageSource) SourceKind() string { return "model" }

func (origin ModelMessageSource) CloneSource() (MessageSource, error) {
	if origin.Provider == "" || origin.Model == "" {
		return nil, errors.New("llm: model message source needs provider and model")
	}
	if len(origin.ReplayState) != 0 {
		detached, err := jsonvalue.Clone(origin.ReplayState)
		if err != nil {
			return nil, fmt.Errorf("llm: invalid model replayState: %w", err)
		}
		origin.ReplayState = detached
	}
	origin.Kind = "model"
	return origin, nil
}

// ToolMessageSource correlates one user-role result with its model call.
type ToolMessageSource struct {
	Kind   string `json:"kind"`
	CallID CallID `json:"callId"`
}

func (ToolMessageSource) SourceKind() string { return "tool" }

func (origin ToolMessageSource) CloneSource() (MessageSource, error) {
	if origin.CallID == "" {
		return nil, errors.New("llm: tool message source needs a callId")
	}
	origin.Kind = "tool"
	return origin, nil
}

// OpaqueMessageSource preserves a plugin-defined source across durable JSON.
type OpaqueMessageSource struct {
	kindName string
	rawValue json.RawMessage
}

// NewOpaqueMessageSource validates and snapshots one extension source.
func NewOpaqueMessageSource(kindName string, rawValue json.RawMessage) (OpaqueMessageSource, error) {
	if kindName == "" {
		return OpaqueMessageSource{}, errors.New("llm: opaque message source kind is empty")
	}
	detached, err := jsonvalue.Clone(rawValue)
	if err != nil || !jsonvalue.IsObject(detached) {
		return OpaqueMessageSource{}, errors.New("llm: opaque message source must be a lossless JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(detached, &fields); err != nil {
		return OpaqueMessageSource{}, err
	}
	var encodedKind string
	if err := json.Unmarshal(fields["kind"], &encodedKind); err != nil || encodedKind != kindName {
		return OpaqueMessageSource{}, errors.New("llm: opaque message source discriminant does not match")
	}
	return OpaqueMessageSource{kindName: kindName, rawValue: detached}, nil
}

func (origin OpaqueMessageSource) SourceKind() string { return origin.kindName }

func (origin OpaqueMessageSource) CloneSource() (MessageSource, error) {
	return NewOpaqueMessageSource(origin.kindName, origin.rawValue)
}

// MarshalJSON returns the original extension object.
func (origin OpaqueMessageSource) MarshalJSON() ([]byte, error) {
	if origin.kindName == "" || len(origin.rawValue) == 0 {
		return nil, errors.New("llm: invalid opaque message source")
	}
	return append([]byte(nil), origin.rawValue...), nil
}

// Message is the immutable value shared by delivery, durable history, and model requests.
type Message interface {
	StableID() MessageID
	ConversationRole() MessageRole
	ContentValue() []ContentBlock
	SourceValue() MessageSource
	CloneMessage() (Message, error)
}

type messageValue struct {
	idValue   MessageID
	roleValue MessageRole
	content   []ContentBlock
	origin    MessageSource
}

// UserMessage is a user-role specialization of Message.
type UserMessage struct{ messageValue }

// AssistantMessage is a model-produced assistant specialization of Message.
type AssistantMessage struct{ messageValue }

// ToolResultMessage is a user-role message containing one correlated tool result.
type ToolResultMessage struct{ messageValue }

// MessageInput contains complete role, content, and source for a new message.
type MessageInput struct {
	Role    MessageRole
	Content []ContentBlock
	Source  MessageSource
}

// UserMessageInput contains content and provenance for a new user message.
type UserMessageInput struct {
	Content []ContentBlock
	Source  MessageSource
}

// AssistantMessageInput contains content and model provenance for a new assistant message.
type AssistantMessageInput struct {
	Content []ContentBlock
	Source  ModelMessageSource
}

// ToolResultMessageInput contains one tool result before message construction.
type ToolResultMessageInput struct {
	CallID  CallID
	Content []ContentBlock
	IsError bool
}

// NewMessage constructs one identified immutable message.
func NewMessage(inputSnapshot MessageInput) (Message, error) {
	identifier, err := generateMessageID()
	if err != nil {
		return nil, err
	}
	entry, err := restoreMessageValue(identifier, inputSnapshot.Role, inputSnapshot.Content, inputSnapshot.Source)
	if err != nil {
		return nil, err
	}
	return entry, nil
}

// NewUserMessage constructs one identified immutable user message.
func NewUserMessage(inputSnapshot UserMessageInput) (UserMessage, error) {
	identifier, err := generateMessageID()
	if err != nil {
		return UserMessage{}, err
	}
	entry, err := restoreMessageValue(identifier, RoleUser, inputSnapshot.Content, inputSnapshot.Source)
	if err != nil {
		return UserMessage{}, err
	}
	return UserMessage{messageValue: entry}, nil
}

// NewAssistantMessage constructs one identified immutable model response.
func NewAssistantMessage(inputSnapshot AssistantMessageInput) (AssistantMessage, error) {
	identifier, err := generateMessageID()
	if err != nil {
		return AssistantMessage{}, err
	}
	entry, err := restoreMessageValue(identifier, RoleAssistant, inputSnapshot.Content, inputSnapshot.Source)
	if err != nil {
		return AssistantMessage{}, err
	}
	return AssistantMessage{messageValue: entry}, nil
}

// NewToolResultMessage constructs one identified correlated tool result.
func NewToolResultMessage(inputSnapshot ToolResultMessageInput) (ToolResultMessage, error) {
	if inputSnapshot.CallID == "" {
		return ToolResultMessage{}, errors.New("llm: tool result needs a callId")
	}
	resultBlock := ToolResultBlock{
		Type: "tool-result", ToolCallID: inputSnapshot.CallID,
		Content: inputSnapshot.Content, IsError: inputSnapshot.IsError, isErrorPresent: true,
	}
	identifier, err := generateMessageID()
	if err != nil {
		return ToolResultMessage{}, err
	}
	entry, err := restoreMessageValue(identifier, RoleUser, []ContentBlock{resultBlock}, ToolMessageSource{CallID: inputSnapshot.CallID})
	if err != nil {
		return ToolResultMessage{}, err
	}
	return ToolResultMessage{messageValue: entry}, nil
}

func generateMessageID() (MessageID, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", fmt.Errorf("llm: generate message id: %w", err)
	}
	randomBytes[6] = randomBytes[6]&0x0f | 0x40
	randomBytes[8] = randomBytes[8]&0x3f | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], randomBytes[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], randomBytes[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], randomBytes[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], randomBytes[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], randomBytes[10:16])
	return MessageID(encoded), nil
}

func restoreMessageValue(identifier MessageID, roleValue MessageRole, content []ContentBlock, origin MessageSource) (messageValue, error) {
	if identifier == "" {
		return messageValue{}, errors.New("llm: message id is empty")
	}
	switch roleValue {
	case RoleSystem, RoleUser, RoleAssistant:
	default:
		return messageValue{}, fmt.Errorf("llm: unsupported message role %q", roleValue)
	}
	if origin == nil || origin.SourceKind() == "" {
		return messageValue{}, errors.New("llm: message source is missing")
	}
	detachedContent, err := CloneContentBlocks(content)
	if err != nil {
		return messageValue{}, err
	}
	detachedOrigin, err := origin.CloneSource()
	if err != nil {
		return messageValue{}, err
	}
	return messageValue{idValue: identifier, roleValue: roleValue, content: detachedContent, origin: detachedOrigin}, nil
}

func (entry messageValue) StableID() MessageID { return entry.idValue }

func (entry messageValue) ConversationRole() MessageRole { return entry.roleValue }

func (entry messageValue) ContentValue() []ContentBlock {
	detached, err := CloneContentBlocks(entry.content)
	if err != nil {
		return nil
	}
	return detached
}

func (entry messageValue) SourceValue() MessageSource {
	if entry.origin == nil {
		return nil
	}
	detached, err := entry.origin.CloneSource()
	if err != nil {
		return nil
	}
	return detached
}

func (entry messageValue) CloneMessage() (Message, error) {
	return restoreMessageValue(entry.idValue, entry.roleValue, entry.content, entry.origin)
}

func (entry messageValue) MarshalJSON() ([]byte, error) {
	if entry.idValue == "" || entry.origin == nil {
		return nil, errors.New("llm: invalid message")
	}
	wireValue := struct {
		ID      MessageID      `json:"id"`
		Role    MessageRole    `json:"role"`
		Content []ContentBlock `json:"content"`
		Source  MessageSource  `json:"source"`
	}{ID: entry.idValue, Role: entry.roleValue, Content: entry.content, Source: entry.origin}
	return json.Marshal(wireValue)
}

// DecodeMessage restores one durable message and preserves unknown source/content extensions.
func DecodeMessage(rawValue json.RawMessage) (Message, error) {
	if err := jsonvalue.Validate(rawValue); err != nil {
		return nil, fmt.Errorf("llm: invalid message JSON: %w", err)
	}
	var wireValue struct {
		ID      MessageID       `json:"id"`
		Role    MessageRole     `json:"role"`
		Content json.RawMessage `json:"content"`
		Source  json.RawMessage `json:"source"`
	}
	if err := decodeStrict(rawValue, &wireValue); err != nil {
		return nil, err
	}
	detachedContent, err := DecodeContentBlocks(wireValue.Content)
	if err != nil {
		return nil, err
	}
	detachedOrigin, err := decodeMessageSource(wireValue.Source)
	if err != nil {
		return nil, err
	}
	entry, err := restoreMessageValue(wireValue.ID, wireValue.Role, detachedContent, detachedOrigin)
	if err != nil {
		return nil, err
	}
	if wireValue.Role == RoleAssistant && detachedOrigin.SourceKind() == "model" {
		return AssistantMessage{messageValue: entry}, nil
	}
	if wireValue.Role == RoleUser && detachedOrigin.SourceKind() == "tool" && len(detachedContent) == 1 {
		if _, ok := detachedContent[0].(ToolResultBlock); ok {
			return ToolResultMessage{messageValue: entry}, nil
		}
	}
	if wireValue.Role == RoleUser {
		return UserMessage{messageValue: entry}, nil
	}
	return entry, nil
}

func decodeMessageSource(rawValue json.RawMessage) (MessageSource, error) {
	if !jsonvalue.IsObject(rawValue) {
		return nil, errors.New("llm: message source must be an object")
	}
	var header struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(rawValue, &header); err != nil || header.Kind == "" {
		return nil, errors.New("llm: message source kind is missing")
	}
	switch header.Kind {
	case "user":
		var origin UserMessageSource
		if err := decodeStrict(rawValue, &origin); err != nil {
			return nil, err
		}
		return origin.CloneSource()
	case "plugin":
		var origin PluginMessageSource
		if err := decodeStrict(rawValue, &origin); err != nil {
			return nil, err
		}
		return origin.CloneSource()
	case "model":
		var origin ModelMessageSource
		if err := decodeStrict(rawValue, &origin); err != nil {
			return nil, err
		}
		return origin.CloneSource()
	case "tool":
		var origin ToolMessageSource
		if err := decodeStrict(rawValue, &origin); err != nil {
			return nil, err
		}
		return origin.CloneSource()
	default:
		return NewOpaqueMessageSource(header.Kind, rawValue)
	}
}

// CloneMessages validates and detaches one ordered conversation.
func CloneMessages(entries []Message) ([]Message, error) {
	detached := make([]Message, len(entries))
	for index, entry := range entries {
		if entry == nil {
			return nil, fmt.Errorf("llm: message %d is nil", index)
		}
		copyValue, err := entry.CloneMessage()
		if err != nil {
			return nil, fmt.Errorf("llm: clone message %d: %w", index, err)
		}
		detached[index] = copyValue
	}
	return detached, nil
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
