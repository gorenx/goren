// Package factory owns strict configuration and construction of the Tools
// Plugin.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/tools"
)

// Factory constructs the canonical root Tools Plugin.
type Factory struct{}

// New constructs a statically linked Tools Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return tools.PluginName
}

// Create strictly decodes configuration and constructs the root Tools
// Service. Runtime dependencies are resolved later during Apply.
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
	validated, err := tools.ValidateConfig(settings)
	if err != nil {
		return nil, err
	}
	return tools.New(validated), nil
}

func decodeConfig(rawConfig json.RawMessage) (tools.Config, error) {
	if err := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"tools factory",
	); err != nil {
		return tools.Config{}, err
	}
	var settings tools.Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	if err := decoder.Decode(&settings); err != nil {
		return tools.Config{}, fmt.Errorf(
			"tools factory: decode configuration: %w",
			err,
		)
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
