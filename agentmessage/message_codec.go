package agentmessage

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/internal/jsonvalue"
)

func (entry messageValue) MarshalJSON() ([]byte, error) {
	if entry.idValue == "" || entry.origin == nil {
		return nil, errors.New("llm: invalid message")
	}
	wireValue := struct {
		ID      MessageID      `json:"id"`
		Role    MessageRole    `json:"role"`
		Content []ContentBlock `json:"content"`
		Source  MessageSource  `json:"source"`
	}{
		ID:      entry.idValue,
		Role:    entry.roleValue,
		Content: entry.content,
		Source:  entry.origin,
	}
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
