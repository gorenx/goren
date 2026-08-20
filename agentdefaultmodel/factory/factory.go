// Package factory owns strict configuration and construction of the default
// Agent model Plugin.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/gorenx/goren/agentdefaultmodel"
	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

// Factory constructs the canonical default Agent model Plugin.
type Factory struct{}

// New constructs a statically linked Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return agentdefaultmodel.PluginName
}

// Create strictly decodes configuration before constructing the Plugin.
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
	selection, err := agentdefaultmodel.ValidateConfig(settings)
	if err != nil {
		return nil, err
	}
	return agentdefaultmodel.NewStatic(selection)
}

func decodeConfig(rawConfig json.RawMessage) (agentdefaultmodel.Config, error) {
	if err := jsonvalue.Validate(rawConfig); err != nil {
		return agentdefaultmodel.Config{}, fmt.Errorf(
			"agentdefaultmodel factory: invalid configuration: %w",
			err,
		)
	}
	if !jsonvalue.IsObject(rawConfig) {
		return agentdefaultmodel.Config{}, errors.New(
			"agentdefaultmodel factory: configuration must be a JSON object",
		)
	}
	var settings agentdefaultmodel.Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return agentdefaultmodel.Config{}, fmt.Errorf(
			"agentdefaultmodel factory: decode configuration: %w",
			err,
		)
	}
	var trailingValue json.RawMessage
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		return agentdefaultmodel.Config{}, errors.New(
			"agentdefaultmodel factory: configuration must contain one JSON value",
		)
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
