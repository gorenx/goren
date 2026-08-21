// Package factory owns strict configuration and construction of the System
// Prompt Plugin.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

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
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
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
	if err := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"systemprompt factory",
	); err != nil {
		return systemprompt.Config{}, err
	}
	var settings systemprompt.Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	if err := decoder.Decode(&settings); err != nil {
		return systemprompt.Config{}, fmt.Errorf(
			"systemprompt factory: decode configuration: %w",
			err,
		)
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
