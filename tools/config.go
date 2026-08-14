package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// PresentationMode controls which tool surface is exposed to the model.
type PresentationMode string

const (
	// PresentationNative exposes each visible tool schema directly.
	PresentationNative PresentationMode = "native"
	// PresentationCode is reserved until the Code Runtime bridge is ported.
	PresentationCode PresentationMode = "code"
	// PresentationBoth is reserved until the Code Runtime bridge is ported.
	PresentationBoth PresentationMode = "both"
)

// Config is the owner-defined typed Tools configuration.
type Config struct {
	Mode                *PresentationMode `json:"mode,omitempty"`
	MaxParallelSubCalls *int              `json:"maxParallelSubCalls,omitempty"`
}

// ValidatedConfig is the immutable result of Tools configuration validation.
type ValidatedConfig struct {
	mode                PresentationMode
	maxParallelSubCalls int
}

// ValidateConfig applies source-compatible defaults and rejects presentation
// modes whose required Code Runtime behavior is not yet present.
func ValidateConfig(settings Config) (ValidatedConfig, error) {
	modeSetting := PresentationNative
	if settings.Mode != nil {
		modeSetting = *settings.Mode
	}
	subCallLimit := 10
	if settings.MaxParallelSubCalls != nil {
		subCallLimit = *settings.MaxParallelSubCalls
		if subCallLimit < 1 {
			return ValidatedConfig{}, errors.New("tools: maxParallelSubCalls must be a positive integer")
		}
	}
	if modeSetting != PresentationNative {
		if modeSetting != PresentationCode && modeSetting != PresentationBoth {
			return ValidatedConfig{}, fmt.Errorf("tools: unsupported presentation mode %q", modeSetting)
		}
		return ValidatedConfig{}, fmt.Errorf("tools: presentation mode %q requires the Code Runtime bridge", modeSetting)
	}
	return ValidatedConfig{mode: modeSetting, maxParallelSubCalls: subCallLimit}, nil
}

type configWire struct {
	Mode                json.RawMessage `json:"mode"`
	MaxParallelSubCalls json.RawMessage `json:"maxParallelSubCalls"`
}

// UnmarshalJSON preserves omission and rejects null, matching the source typed
// configuration contract.
func (settings *Config) UnmarshalJSON(encoded []byte) error {
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return errors.New("tools: config must be an object")
	}
	var wire configWire
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	var decoded Config
	if len(wire.Mode) > 0 {
		if bytes.Equal(bytes.TrimSpace(wire.Mode), []byte("null")) {
			return errors.New("tools: mode must be native, code, or both")
		}
		var selected PresentationMode
		if err := json.Unmarshal(wire.Mode, &selected); err != nil {
			return fmt.Errorf("tools: mode must be native, code, or both: %w", err)
		}
		decoded.Mode = &selected
	}
	if len(wire.MaxParallelSubCalls) > 0 {
		if bytes.Equal(bytes.TrimSpace(wire.MaxParallelSubCalls), []byte("null")) {
			return errors.New("tools: maxParallelSubCalls must be a positive integer")
		}
		var selected int
		if err := json.Unmarshal(wire.MaxParallelSubCalls, &selected); err != nil {
			return fmt.Errorf("tools: maxParallelSubCalls must be a positive integer: %w", err)
		}
		decoded.MaxParallelSubCalls = &selected
	}
	*settings = decoded
	return nil
}
