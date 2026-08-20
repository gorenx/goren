// Package factory owns strict configuration and construction of the Agent
// Loop Plugin.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/internal/jsonvalue"
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
	if err := createContext.Err(); err != nil {
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
	return agentloop.New(resolved, builder.runtimeOptions)
}

func decodeConfig(rawConfig json.RawMessage) (Config, error) {
	if err := jsonvalue.Validate(rawConfig); err != nil {
		return Config{}, fmt.Errorf(
			"agentloop factory: invalid configuration: %w",
			err,
		)
	}
	if !jsonvalue.IsObject(rawConfig) {
		return Config{}, errors.New(
			"agentloop factory: configuration must be a JSON object",
		)
	}
	var settings Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	if err := decoder.Decode(&settings); err != nil {
		return Config{}, fmt.Errorf(
			"agentloop factory: decode configuration: %w",
			err,
		)
	}
	var trailingValue json.RawMessage
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		return Config{}, errors.New(
			"agentloop factory: configuration must contain one JSON value",
		)
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
