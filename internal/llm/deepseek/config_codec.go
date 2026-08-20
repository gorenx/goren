package deepseek

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type configWire struct {
	APIKeyEnv            json.RawMessage `json:"apiKeyEnv"`
	BaseURL              json.RawMessage `json:"baseURL"`
	Thinking             json.RawMessage `json:"thinking"`
	ReasoningEffort      json.RawMessage `json:"reasoningEffort"`
	MaxTokens            json.RawMessage `json:"maxTokens"`
	DefaultContextWindow json.RawMessage `json:"defaultContextWindow"`
	Models               json.RawMessage `json:"models"`
	StreamIdleTimeoutMS  json.RawMessage `json:"streamIdleTimeoutMs"`
	RetryPolicy          json.RawMessage `json:"retryPolicy"`
}

type catalogModelWire struct {
	ID            json.RawMessage `json:"id"`
	Name          json.RawMessage `json:"name"`
	Description   json.RawMessage `json:"description"`
	ContextWindow json.RawMessage `json:"contextWindow"`
	MaxTokens     json.RawMessage `json:"maxTokens"`
}

// UnmarshalJSON keeps optional catalog fields distinguishable from explicit
// null or empty values and rejects ownerless extension keys.
func (catalogEntry *CatalogModel) UnmarshalJSON(encoded []byte) error {
	if catalogEntry == nil {
		return errors.New("llm-deepseek: cannot decode catalog model into nil target")
	}
	if isNull(encoded) {
		return errors.New("llm-deepseek: catalog model must be an object")
	}
	var wireValue catalogModelWire
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wireValue); err != nil {
		return err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	if len(wireValue.ID) == 0 || isNull(wireValue.ID) {
		return errors.New("llm-deepseek: catalog model id is required")
	}
	var decoded CatalogModel
	if err := json.Unmarshal(wireValue.ID, &decoded.ID); err != nil {
		return errors.New("llm-deepseek: catalog model id must be a string")
	}
	if err := decodeOptional(wireValue.Name, "catalog model name", &decoded.Name); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.Description, "catalog model description", &decoded.Description); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.ContextWindow, "catalog model contextWindow", &decoded.ContextWindow); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.MaxTokens, "catalog model maxTokens", &decoded.MaxTokens); err != nil {
		return err
	}
	*catalogEntry = decoded
	return nil
}

// UnmarshalJSON rejects null and unknown fields while preserving omission.
func (settings *Config) UnmarshalJSON(encoded []byte) error {
	if settings == nil {
		return errors.New("llm-deepseek: cannot decode config into nil target")
	}
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return errors.New("llm-deepseek: config must be an object")
	}
	var wireValue configWire
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wireValue); err != nil {
		return err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	var decoded Config
	if err := decodeOptional(wireValue.APIKeyEnv, "apiKeyEnv", &decoded.APIKeyEnv); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.BaseURL, "baseURL", &decoded.BaseURL); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.Thinking, "thinking", &decoded.Thinking); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.ReasoningEffort, "reasoningEffort", &decoded.ReasoningEffort); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.MaxTokens, "maxTokens", &decoded.MaxTokens); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.DefaultContextWindow, "defaultContextWindow", &decoded.DefaultContextWindow); err != nil {
		return err
	}
	if len(wireValue.Models) != 0 {
		if isNull(wireValue.Models) {
			return errors.New("llm-deepseek: models must be an array")
		}
		var models []CatalogModel
		if err := json.Unmarshal(wireValue.Models, &models); err != nil {
			return fmt.Errorf("llm-deepseek: models must be an array: %w", err)
		}
		decoded.Models = &models
	}
	if err := decodeOptional(wireValue.StreamIdleTimeoutMS, "streamIdleTimeoutMs", &decoded.StreamIdleTimeoutMS); err != nil {
		return err
	}
	if err := decodeOptional(wireValue.RetryPolicy, "retryPolicy", &decoded.RetryPolicy); err != nil {
		return err
	}
	*settings = decoded
	return nil
}

func decodeOptional[T any](rawValue json.RawMessage, fieldName string, destination **T) error {
	if len(rawValue) == 0 {
		return nil
	}
	if isNull(rawValue) {
		return fmt.Errorf("llm-deepseek: %s must not be null", fieldName)
	}
	var decoded T
	if err := json.Unmarshal(rawValue, &decoded); err != nil {
		return fmt.Errorf("llm-deepseek: invalid %s: %w", fieldName, err)
	}
	*destination = &decoded
	return nil
}

func isNull(rawValue json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(rawValue), []byte("null"))
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("llm-deepseek: unexpected trailing JSON")
		}
		return err
	}
	return nil
}
