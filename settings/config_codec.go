package settings

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/tools"
)

// DecodeProvider decodes a complete connection group at a JSON erasure boundary.
func DecodeProvider(encoded []byte) (ProviderConfig, error) {
	var decoded ProviderConfig
	if err := decodeConfiguration(encoded, &decoded); err != nil {
		return ProviderConfig{}, err
	}
	return decoded, nil
}

// DecodePrompt decodes a complete prompt group, preserving false and empty strings.
func DecodePrompt(encoded []byte) (PromptConfig, error) {
	var decoded PromptConfig
	if err := decodeConfiguration(encoded, &decoded); err != nil {
		return PromptConfig{}, err
	}
	return decoded, nil
}

// DecodeMCP decodes one complete user-editable HTTP connection group.
func DecodeMCP(encoded []byte) (MCPConfig, error) {
	var decoded MCPConfig
	if err := decodeConfiguration(encoded, &decoded); err != nil {
		return MCPConfig{}, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(encoded, &fields) != nil || len(fields) != 2 {
		return MCPConfig{}, ErrInvalid
	}
	if err := decoded.Validate(decoded.Connection.ServerName); err != nil {
		return MCPConfig{}, err
	}
	return decoded, nil
}

// DecodeTool decodes one complete editable tool configuration.
func DecodeTool(encoded []byte) (tools.Config, error) {
	var decoded tools.Config
	if err := decodeConfiguration(encoded, &decoded); err != nil {
		return tools.Config{}, err
	}
	return decoded, nil
}

func decodeConfiguration(encoded []byte, destination any) error {
	if !jsonvalue.IsObject(encoded) {
		return ErrInvalid
	}
	if err := validateConfiguration(encoded, reflect.TypeOf(destination)); err != nil {
		return err
	}
	if json.Unmarshal(encoded, destination) != nil {
		return ErrInvalid
	}
	return nil
}

// validateConfiguration checks canonical keys before encoding/json can accept
// case-insensitive field names. Raw values exist only during decoding.
func validateConfiguration(encoded json.RawMessage, shape reflect.Type) error {
	if strings.TrimSpace(string(encoded)) == "null" {
		return ErrInvalid
	}
	for shape.Kind() == reflect.Pointer {
		shape = shape.Elem()
	}
	switch shape.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(encoded, &fields) != nil || fields == nil {
			return ErrInvalid
		}
		for name, value := range fields {
			var target reflect.Type
			for index := 0; index < shape.NumField(); index++ {
				field := shape.Field(index)
				canonical := strings.Split(field.Tag.Get("json"), ",")[0]
				if canonical == name {
					target = field.Type
					break
				}
			}
			if target == nil {
				return ErrInvalid
			}
			if err := validateConfiguration(value, target); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var entries []json.RawMessage
		if json.Unmarshal(encoded, &entries) != nil {
			return ErrInvalid
		}
		for _, value := range entries {
			if err := validateConfiguration(value, shape.Elem()); err != nil {
				return err
			}
		}
	default:
		if json.Unmarshal(encoded, reflect.New(shape).Interface()) != nil {
			return ErrInvalid
		}
	}
	return nil
}
