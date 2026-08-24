// Package factory owns strict Commands Plugin construction.
package factory

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

// Factory constructs the canonical Commands Plugin.
type Factory struct {
	options commands.RuntimeOptions
}

// New constructs a statically linked Commands Factory.
func New(options commands.RuntimeOptions) *Factory {
	return &Factory{
		options: options,
	}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return commands.PluginName
}

// Create validates empty owner-defined configuration.
func (builder *Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	if err := pluginfactory.ValidateEmptyConfig(rawConfig, "commands factory"); err != nil {
		return nil, err
	}
	return commands.New(builder.options)
}

var _ pluginfactory.Factory = (*Factory)(nil)
