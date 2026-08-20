// Package factory owns strict configuration and construction of the System
// Prompt Plugin.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/systemprompt"
)

// Factory constructs the canonical root System Prompt Plugin.
type Factory struct{}

// New constructs a statically linked System Prompt Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return systemprompt.PluginName
}

// Create strictly decodes configuration and constructs the root Registry.
func (*Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := createContext.Err(); err != nil {
		return nil, err
	}
	settings, err := decodeConfig(rawConfig)
	if err != nil {
		return nil, err
	}
	validated, err := systemprompt.ValidateConfig(settings)
	if err != nil {
		return nil, err
	}
	return systemprompt.New(
		validated,
		systemprompt.RegistryOptions{},
	), nil
}

func decodeConfig(rawConfig json.RawMessage) (systemprompt.Config, error) {
	if err := jsonvalue.Validate(rawConfig); err != nil {
		return systemprompt.Config{}, fmt.Errorf(
			"systemprompt factory: invalid configuration: %w",
			err,
		)
	}
	if !jsonvalue.IsObject(rawConfig) {
		return systemprompt.Config{}, errors.New(
			"systemprompt factory: configuration must be a JSON object",
		)
	}
	var settings systemprompt.Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	if err := decoder.Decode(&settings); err != nil {
		return systemprompt.Config{}, fmt.Errorf(
			"systemprompt factory: decode configuration: %w",
			err,
		)
	}
	var trailingValue json.RawMessage
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		return systemprompt.Config{}, errors.New(
			"systemprompt factory: configuration must contain one JSON value",
		)
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
