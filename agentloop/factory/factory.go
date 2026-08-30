// Package factory owns strict configuration and construction of the Agent
// Loop Plugin.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

// Factory constructs the canonical root Agent Loop Plugin.
type Factory struct {
	runtimeOptions agentloop.RuntimeOptions
}

// New constructs a statically linked Agent Loop Factory.
func New(runtimeOptions agentloop.RuntimeOptions) *Factory {
	return &Factory{
		runtimeOptions: runtimeOptions,
	}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return agentloop.PluginName
}

// Create strictly decodes and validates configuration before constructing an
// inactive Plugin. Raw JSON never reaches Agent Loop runtime logic.
func (builder *Factory) Create(
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
	resolved, err := resolveSettings(settings)
	if err != nil {
		return nil, err
	}
	return agentloop.NewPlugin(resolved, builder.runtimeOptions)
}

func decodeConfig(rawConfig json.RawMessage) (Config, error) {
	if err := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"agentloop factory",
	); err != nil {
		return Config{}, err
	}
	var settings Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	if err := decoder.Decode(&settings); err != nil {
		return Config{}, fmt.Errorf(
			"agentloop factory: decode configuration: %w",
			err,
		)
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
