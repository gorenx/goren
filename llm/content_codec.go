package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/gorenx/goren/internal/jsonvalue"
)

// DecodeContentBlocks restores core variants and losslessly preserves unknown
// plugin variants as OpaqueContentBlock values.
func DecodeContentBlocks(rawValue json.RawMessage) ([]ContentBlock, error) {
	if err := jsonvalue.Validate(rawValue); err != nil {
		return nil, fmt.Errorf("llm: invalid content JSON: %w", err)
	}
	var encoded []json.RawMessage
	if err := json.Unmarshal(rawValue, &encoded); err != nil {
		return nil, errors.New("llm: content must be an array")
	}
	blocks := make([]ContentBlock, 0, len(encoded))
	for index, blockJSON := range encoded {
		entry, err := decodeContentBlock(blockJSON)
		if err != nil {
			return nil, fmt.Errorf("llm: content block %d: %w", index, err)
		}
		blocks = append(blocks, entry)
	}
	return blocks, nil
}

func decodeContentBlock(rawValue json.RawMessage) (ContentBlock, error) {
	if !jsonvalue.IsObject(rawValue) {
		return nil, errors.New("content block must be an object")
	}
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(rawValue, &header); err != nil || header.Type == "" {
		return nil, errors.New("content block type is missing")
	}
	switch header.Type {
	case "text":
		var entry TextBlock
		if err := decodeStrict(rawValue, &entry); err != nil {
			return NewOpaqueContentBlock(header.Type, rawValue)
		}
		return entry, nil
	case "reasoning":
		var entry ReasoningBlock
		if err := decodeStrict(rawValue, &entry); err != nil {
			return NewOpaqueContentBlock(header.Type, rawValue)
		}
		return entry, nil
	case "image":
		var entry ImageBlock
		if err := decodeStrict(rawValue, &entry); err != nil {
			return NewOpaqueContentBlock(header.Type, rawValue)
		}
		return entry, nil
	case "tool-call":
		var entry ToolCallBlock
		if err := decodeStrict(rawValue, &entry); err != nil {
			return NewOpaqueContentBlock(header.Type, rawValue)
		}
		return entry, nil
	case "tool-result":
		var entry ToolResultBlock
		if err := json.Unmarshal(rawValue, &entry); err != nil {
			return NewOpaqueContentBlock(header.Type, rawValue)
		}
		return entry, nil
	default:
		return NewOpaqueContentBlock(header.Type, rawValue)
	}
}

// UnmarshalJSON restores the nested content sequence of a tool-result block.
func (entry *ToolResultBlock) UnmarshalJSON(rawValue []byte) error {
	if entry == nil {
		return errors.New("llm: cannot decode tool-result into nil target")
	}
	var encoded struct {
		Type       string          `json:"type"`
		ToolCallID CallID          `json:"toolCallId"`
		Content    json.RawMessage `json:"content"`
		IsError    json.RawMessage `json:"isError"`
	}
	if err := decodeStrict(rawValue, &encoded); err != nil {
		return err
	}
	if encoded.Type != "tool-result" {
		return errors.New("llm: invalid tool-result discriminant")
	}
	nested, err := DecodeContentBlocks(encoded.Content)
	if err != nil {
		return err
	}
	isError := false
	isErrorPresent := len(encoded.IsError) != 0
	if isErrorPresent {
		if bytes.Equal(bytes.TrimSpace(encoded.IsError), []byte("null")) || json.Unmarshal(encoded.IsError, &isError) != nil {
			return errors.New("llm: tool-result isError must be a boolean")
		}
	}
	*entry = ToolResultBlock{
		Type: "tool-result", ToolCallID: encoded.ToolCallID, Content: nested,
		IsError: isError, isErrorPresent: isErrorPresent,
	}
	return nil
}

func decodeStrict[T any](rawValue []byte, destination *T) error {
	if err := jsonvalue.Validate(rawValue); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("llm: unexpected trailing JSON")
		}
		return err
	}
	return nil
}
