// Package factory owns strict configuration and construction of the Agent
// Registry Plugin.
package factory

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

// Factory constructs the canonical Agent Registry Plugin.
type Factory struct {
	runtimeOptions agent.RegistryOptions
}

// New constructs a statically linked Factory.
func New(runtimeOptions agent.RegistryOptions) *Factory {
	return &Factory{
		runtimeOptions: runtimeOptions,
	}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return agent.PluginName
}

// Create strictly decodes configuration and constructs the Agent Registry.
func (builder *Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	if err := pluginfactory.ValidateEmptyConfig(
		rawConfig,
		"agent factory",
	); err != nil {
		return nil, err
	}
	registry := agent.NewRegistry(builder.runtimeOptions)
	return agent.NewRegistryPlugin(registry)
}

var _ pluginfactory.Factory = (*Factory)(nil)
