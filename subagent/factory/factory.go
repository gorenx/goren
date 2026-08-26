// Package factory owns strict configuration and construction of the Subagent
// Plugin.
package factory

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/subagent"
	subagentplugin "github.com/gorenx/goren/subagent/plugin"
)

// Factory constructs the canonical Subagent Plugin.
type Factory struct {
	diagnostics subagentplugin.Diagnostics
}

// New constructs a statically linked Factory.
func New(diagnostics subagentplugin.Diagnostics) *Factory {
	return &Factory{
		diagnostics: diagnostics,
	}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return subagent.PluginName
}

// Create validates the empty configuration and constructs an inactive Plugin.
func (builder *Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	if err := pluginfactory.ValidateEmptyConfig(
		rawConfig,
		"subagent factory",
	); err != nil {
		return nil, err
	}
	return subagentplugin.New(builder.diagnostics), nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
