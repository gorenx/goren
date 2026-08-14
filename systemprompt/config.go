package systemprompt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// ValidateConfig applies defaults and rejects invalid tool ordering before a
// runtime Plugin is constructed.
func ValidateConfig(settings Config) (ValidatedConfig, error) {
	configuredOrder, err := validateToolOrder(settings.ToolOrder)
	if err != nil {
		return ValidatedConfig{}, err
	}
	return ValidatedConfig{
		includeHarnessIdentity: optionEnabled(settings.IncludeHarnessIdentity, true),
		includeRuntimeContext:  optionEnabled(settings.IncludeRuntimeContext, true),
		persona:                settings.Persona,
		toolOrder:              configuredOrder,
	}, nil
}

func optionEnabled(selected *bool, fallback bool) bool {
	if selected == nil {
		return fallback
	}
	return *selected
}

type configWire struct {
	IncludeHarnessIdentity json.RawMessage `json:"includeHarnessIdentity"`
	IncludeRuntimeContext  json.RawMessage `json:"includeRuntimeContext"`
	Persona                json.RawMessage `json:"persona"`
	ToolOrder              json.RawMessage `json:"toolOrder"`
}

// UnmarshalJSON preserves omission while rejecting null for every optional
// field, matching the source typed-config schema.
func (settings *Config) UnmarshalJSON(encoded []byte) error {
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return errors.New("systemprompt: config must be an object")
	}
	var wire configWire
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	var decoded Config
	if len(wire.IncludeHarnessIdentity) > 0 {
		selected, err := decodeConfigBool(wire.IncludeHarnessIdentity, "includeHarnessIdentity")
		if err != nil {
			return err
		}
		decoded.IncludeHarnessIdentity = &selected
	}
	if len(wire.IncludeRuntimeContext) > 0 {
		selected, err := decodeConfigBool(wire.IncludeRuntimeContext, "includeRuntimeContext")
		if err != nil {
			return err
		}
		decoded.IncludeRuntimeContext = &selected
	}
	if len(wire.Persona) > 0 {
		if bytes.Equal(bytes.TrimSpace(wire.Persona), []byte("null")) || json.Unmarshal(wire.Persona, &decoded.Persona) != nil {
			return errors.New("systemprompt: persona must be a string")
		}
	}
	if len(wire.ToolOrder) > 0 {
		if bytes.Equal(bytes.TrimSpace(wire.ToolOrder), []byte("null")) {
			return errors.New("systemprompt: toolOrder must be an array of strings")
		}
		if err := json.Unmarshal(wire.ToolOrder, &decoded.ToolOrder); err != nil {
			return fmt.Errorf("systemprompt: toolOrder must be an array of strings: %w", err)
		}
	}
	*settings = decoded
	return nil
}

// MarshalJSON preserves the distinction between omitted and explicitly empty
// toolOrder, because an empty configured order is invalid.
func (settings Config) MarshalJSON() ([]byte, error) {
	type encodedConfig struct {
		IncludeHarnessIdentity *bool     `json:"includeHarnessIdentity,omitempty"`
		IncludeRuntimeContext  *bool     `json:"includeRuntimeContext,omitempty"`
		Persona                string    `json:"persona,omitempty"`
		ToolOrder              *[]string `json:"toolOrder,omitempty"`
	}
	var requestedOrder *[]string
	if settings.ToolOrder != nil {
		retainedOrder := slices.Clone(settings.ToolOrder)
		requestedOrder = &retainedOrder
	}
	return json.Marshal(encodedConfig{
		IncludeHarnessIdentity: settings.IncludeHarnessIdentity,
		IncludeRuntimeContext:  settings.IncludeRuntimeContext,
		Persona:                settings.Persona,
		ToolOrder:              requestedOrder,
	})
}

func decodeConfigBool(encoded json.RawMessage, fieldName string) (bool, error) {
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return false, fmt.Errorf("systemprompt: %s must be a boolean", fieldName)
	}
	var selected bool
	if err := json.Unmarshal(encoded, &selected); err != nil {
		return false, fmt.Errorf("systemprompt: %s must be a boolean: %w", fieldName, err)
	}
	return selected, nil
}
