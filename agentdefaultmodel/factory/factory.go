// Package factory owns strict configuration and construction of the default
// Agent model Plugin.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/agentdefaultmodel"
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
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
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
	if err := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"agentdefaultmodel factory",
	); err != nil {
		return agentdefaultmodel.Config{}, err
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
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
