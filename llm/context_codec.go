package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// ContextSchemaVersion is the current stable Context wire schema.
const ContextSchemaVersion = 1

type contextWire struct {
	Version      int           `json:"version"`
	SystemPrompt string        `json:"system_prompt,omitempty"`
	Messages     []messageWire `json:"messages"`
	Tools        []Tool        `json:"tools,omitempty"`
}

type messageWire struct {
	Role          Role          `json:"role"`
	Content       []contentWire `json:"content"`
	Timestamp     time.Time     `json:"timestamp"`
	API           API           `json:"api,omitempty"`
	Provider      Provider      `json:"provider,omitempty"`
	ModelID       string        `json:"model,omitempty"`
	ResponseModel string        `json:"response_model,omitempty"`
	ResponseID    string        `json:"response_id,omitempty"`
	Usage         Usage         `json:"usage,omitempty"`
	StopReason    StopReason    `json:"stop_reason,omitempty"`
	ErrorMessage  string        `json:"error_message,omitempty"`
	ToolCallID    string        `json:"tool_call_id,omitempty"`
	ToolName      string        `json:"tool_name,omitempty"`
	IsError       bool          `json:"is_error,omitempty"`
}

type contentWire struct {
	Type      string             `json:"type"`
	Text      string             `json:"text,omitempty"`
	Phase     AssistantTextPhase `json:"phase,omitempty"`
	Data      string             `json:"data,omitempty"`
	MIMEType  string             `json:"mime_type,omitempty"`
	Thinking  string             `json:"thinking,omitempty"`
	Signature string             `json:"signature,omitempty"`
	Redacted  bool               `json:"redacted,omitempty"`
	ID        string             `json:"id,omitempty"`
	Name      string             `json:"name,omitempty"`
	Arguments json.RawMessage    `json:"arguments,omitempty"`
	Metadata  *ReplayMetadata    `json:"metadata,omitempty"`
}

// MarshalJSON serializes Context using the versioned provider-neutral wire schema.
func (input Context) MarshalJSON() ([]byte, error) {
	wireValue, err := encodeContext(input)
	if err != nil {
		return nil, err
	}
	return json.Marshal(wireValue)
}

// UnmarshalJSON restores Context from the versioned provider-neutral wire schema.
func (input *Context) UnmarshalJSON(data []byte) error {
	if input == nil {
		return errors.New("cannot unmarshal Context into nil receiver")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wireValue contextWire
	if err := decoder.Decode(&wireValue); err != nil {
		return fmt.Errorf("decode LLM context: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	decoded, err := decodeContext(wireValue)
	if err != nil {
		return err
	}
	*input = decoded
	return nil
}

func encodeContext(input Context) (contextWire, error) {
	wireValue := contextWire{
		Version:      ContextSchemaVersion,
		SystemPrompt: input.SystemPrompt,
		Messages:     make([]messageWire, 0, len(input.Messages)),
		Tools:        cloneTools(input.Tools),
	}
	for messageIndex, conversationEntry := range input.Messages {
		encoded, err := encodeMessage(conversationEntry)
		if err != nil {
			return contextWire{}, fmt.Errorf("encode message %d: %w", messageIndex, err)
		}
		wireValue.Messages = append(wireValue.Messages, encoded)
	}
	return wireValue, nil
}

func encodeMessage(conversationEntry Message) (messageWire, error) {
	switch typedMessage := conversationEntry.(type) {
	case UserMessage:
		content, err := encodeUserContent(typedMessage.Content)
		return messageWire{Role: RoleUser, Content: content, Timestamp: typedMessage.Timestamp}, err
	case AssistantMessage:
		content, err := encodeAssistantContent(typedMessage.Content)
		return messageWire{
			Role:          RoleAssistant,
			Content:       content,
			Timestamp:     typedMessage.Timestamp,
			API:           typedMessage.API,
			Provider:      typedMessage.Provider,
			ModelID:       typedMessage.Model,
			ResponseModel: typedMessage.ResponseModel,
			ResponseID:    typedMessage.ResponseID,
			Usage:         typedMessage.Usage,
			StopReason:    typedMessage.StopReason,
			ErrorMessage:  typedMessage.ErrorMessage,
		}, err
	case ToolResultMessage:
		content, err := encodeToolResultContent(typedMessage.Content)
		return messageWire{
			Role:       RoleToolResult,
			Content:    content,
			Timestamp:  typedMessage.Timestamp,
			ToolCallID: typedMessage.ToolCallID,
			ToolName:   typedMessage.ToolName,
			IsError:    typedMessage.IsError,
		}, err
	default:
		return messageWire{}, fmt.Errorf("unsupported message type %T", conversationEntry)
	}
}

func encodeUserContent(content []UserContent) ([]contentWire, error) {
	encoded := make([]contentWire, 0, len(content))
	for _, contentEntry := range content {
		switch typedContent := contentEntry.(type) {
		case TextContent:
			encoded = append(encoded, contentWire{Type: "text", Text: typedContent.Text})
		case ImageContent:
			encoded = append(encoded, contentWire{Type: "image", Data: typedContent.Data, MIMEType: typedContent.MIMEType})
		default:
			return nil, fmt.Errorf("unsupported user content %T", contentEntry)
		}
	}
	return encoded, nil
}

func encodeAssistantContent(content []AssistantContent) ([]contentWire, error) {
	encoded := make([]contentWire, 0, len(content))
	for _, contentEntry := range content {
		switch typedContent := contentEntry.(type) {
		case AssistantTextContent:
			encoded = append(encoded, contentWire{Type: "text", Text: typedContent.Text, Phase: typedContent.Phase, Metadata: cloneReplayMetadata(typedContent.Metadata)})
		case ThinkingContent:
			encoded = append(encoded, contentWire{Type: "thinking", Thinking: typedContent.Thinking, Signature: typedContent.Signature, Redacted: typedContent.Redacted, Metadata: cloneReplayMetadata(typedContent.Metadata)})
		case ToolCall:
			encoded = append(encoded, contentWire{Type: "tool_call", ID: typedContent.ID, Name: typedContent.Name, Arguments: cloneRawMessage(typedContent.Arguments), Metadata: cloneReplayMetadata(typedContent.Metadata)})
		default:
			return nil, fmt.Errorf("unsupported assistant content %T", contentEntry)
		}
	}
	return encoded, nil
}

func encodeToolResultContent(content []ToolResultContent) ([]contentWire, error) {
	encoded := make([]contentWire, 0, len(content))
	for _, contentEntry := range content {
		switch typedContent := contentEntry.(type) {
		case TextContent:
			encoded = append(encoded, contentWire{Type: "text", Text: typedContent.Text})
		case ImageContent:
			encoded = append(encoded, contentWire{Type: "image", Data: typedContent.Data, MIMEType: typedContent.MIMEType})
		default:
			return nil, fmt.Errorf("unsupported tool-result content %T", contentEntry)
		}
	}
	return encoded, nil
}

func decodeContext(wireValue contextWire) (Context, error) {
	if wireValue.Version != ContextSchemaVersion {
		return Context{}, fmt.Errorf("unsupported LLM context schema version %d", wireValue.Version)
	}
	decoded := Context{
		SystemPrompt: wireValue.SystemPrompt,
		Messages:     make([]Message, 0, len(wireValue.Messages)),
		Tools:        cloneTools(wireValue.Tools),
	}
	for messageIndex, encodedMessage := range wireValue.Messages {
		conversationEntry, err := decodeMessage(encodedMessage)
		if err != nil {
			return Context{}, fmt.Errorf("decode message %d: %w", messageIndex, err)
		}
		decoded.Messages = append(decoded.Messages, conversationEntry)
	}
	return decoded, nil
}

func decodeMessage(encoded messageWire) (Message, error) {
	switch encoded.Role {
	case RoleUser:
		content, err := decodeUserContent(encoded.Content)
		return UserMessage{Content: content, Timestamp: encoded.Timestamp}, err
	case RoleAssistant:
		content, err := decodeAssistantContent(encoded.Content)
		return AssistantMessage{
			Content:       content,
			API:           encoded.API,
			Provider:      encoded.Provider,
			Model:         encoded.ModelID,
			ResponseModel: encoded.ResponseModel,
			ResponseID:    encoded.ResponseID,
			Usage:         encoded.Usage,
			StopReason:    encoded.StopReason,
			ErrorMessage:  encoded.ErrorMessage,
			Timestamp:     encoded.Timestamp,
		}, err
	case RoleToolResult:
		content, err := decodeToolResultContent(encoded.Content)
		return ToolResultMessage{ToolCallID: encoded.ToolCallID, ToolName: encoded.ToolName, Content: content, IsError: encoded.IsError, Timestamp: encoded.Timestamp}, err
	default:
		return nil, fmt.Errorf("unsupported message role %q", encoded.Role)
	}
}

func decodeUserContent(encoded []contentWire) ([]UserContent, error) {
	content := make([]UserContent, 0, len(encoded))
	for _, encodedContent := range encoded {
		switch encodedContent.Type {
		case "text":
			content = append(content, TextContent{Text: encodedContent.Text})
		case "image":
			content = append(content, ImageContent{Data: encodedContent.Data, MIMEType: encodedContent.MIMEType})
		default:
			return nil, fmt.Errorf("unsupported user content type %q", encodedContent.Type)
		}
	}
	return content, nil
}

func decodeAssistantContent(encoded []contentWire) ([]AssistantContent, error) {
	content := make([]AssistantContent, 0, len(encoded))
	for _, encodedContent := range encoded {
		switch encodedContent.Type {
		case "text":
			content = append(content, AssistantTextContent{Text: encodedContent.Text, Phase: encodedContent.Phase, Metadata: cloneReplayMetadata(encodedContent.Metadata)})
		case "thinking":
			content = append(content, ThinkingContent{Thinking: encodedContent.Thinking, Signature: encodedContent.Signature, Redacted: encodedContent.Redacted, Metadata: cloneReplayMetadata(encodedContent.Metadata)})
		case "tool_call":
			content = append(content, ToolCall{ID: encodedContent.ID, Name: encodedContent.Name, Arguments: cloneRawMessage(encodedContent.Arguments), Metadata: cloneReplayMetadata(encodedContent.Metadata)})
		default:
			return nil, fmt.Errorf("unsupported assistant content type %q", encodedContent.Type)
		}
	}
	return content, nil
}

func decodeToolResultContent(encoded []contentWire) ([]ToolResultContent, error) {
	content := make([]ToolResultContent, 0, len(encoded))
	for _, encodedContent := range encoded {
		switch encodedContent.Type {
		case "text":
			content = append(content, TextContent{Text: encodedContent.Text})
		case "image":
			content = append(content, ImageContent{Data: encodedContent.Data, MIMEType: encodedContent.MIMEType})
		default:
			return nil, fmt.Errorf("unsupported tool-result content type %q", encodedContent.Type)
		}
	}
	return content, nil
}

func cloneReplayMetadata(metadata *ReplayMetadata) *ReplayMetadata {
	if metadata == nil {
		return nil
	}
	cloned := *metadata
	cloned.Data = cloneRawMessage(metadata.Data)
	return &cloned
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode trailing LLM context data: %w", err)
	}
	return errors.New("LLM context contains multiple JSON values")
}
