// Package factory constructs the built-in Session-turn Bound input source.
package factory

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/subagent/bound/turnrelay"
)

// Factory constructs the statically linked Turn Relay Plugin.
type Factory struct {
	diagnostics turnrelay.Diagnostics
}

// New constructs a Factory.
func New(diagnostics turnrelay.Diagnostics) *Factory {
	return &Factory{
		diagnostics: diagnostics,
	}
}

// Name returns the Plugin identity.
func (*Factory) Name() string {
	return turnrelay.PluginName
}

// Create validates the empty configuration and constructs the Plugin.
func (owner *Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	if err := pluginfactory.ValidateEmptyConfig(
		rawConfig,
		"Bound Turn Relay factory",
	); err != nil {
		return nil, err
	}
	return turnrelay.New(owner.diagnostics), nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
