package llm

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

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

// CloneUserMessage returns a detached user-role message while preserving its
// stable identity and specialized Go type.
func CloneUserMessage(source UserMessage) (UserMessage, error) {
	copyValue, err := restoreMessageValue(
		source.idValue, source.roleValue, source.content, source.origin,
	)
	if err != nil {
		return UserMessage{}, err
	}
	if copyValue.roleValue != RoleUser {
		return UserMessage{}, errors.New("llm: message is not a user message")
	}
	return UserMessage{messageValue: copyValue}, nil
}

// CloneAssistantMessage returns a detached model-produced assistant message.
func CloneAssistantMessage(source AssistantMessage) (AssistantMessage, error) {
	copyValue, err := restoreMessageValue(
		source.idValue, source.roleValue, source.content, source.origin,
	)
	if err != nil {
		return AssistantMessage{}, err
	}
	if copyValue.roleValue != RoleAssistant || copyValue.origin.SourceKind() != "model" {
		return AssistantMessage{}, errors.New("llm: message is not a model assistant message")
	}
	return AssistantMessage{messageValue: copyValue}, nil
}

// CloneToolResultMessage returns a detached correlated tool-result message.
func CloneToolResultMessage(source ToolResultMessage) (ToolResultMessage, error) {
	copyValue, err := restoreMessageValue(
		source.idValue, source.roleValue, source.content, source.origin,
	)
	if err != nil {
		return ToolResultMessage{}, err
	}
	if copyValue.roleValue != RoleUser || copyValue.origin.SourceKind() != "tool" || len(copyValue.content) != 1 {
		return ToolResultMessage{}, errors.New("llm: message is not a tool-result message")
	}
	if _, ok := copyValue.content[0].(ToolResultBlock); !ok {
		return ToolResultMessage{}, errors.New("llm: message is not a tool-result message")
	}
	return ToolResultMessage{messageValue: copyValue}, nil
}

// DecodeUserMessage restores one durable user message and rejects other roles.
func DecodeUserMessage(rawValue json.RawMessage) (UserMessage, error) {
	restored, err := DecodeMessage(rawValue)
	if err != nil {
		return UserMessage{}, err
	}
	typedValue, ok := restored.(UserMessage)
	if !ok {
		return UserMessage{}, errors.New("llm: durable message is not a user message")
	}
	return CloneUserMessage(typedValue)
}

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
