// Package factory owns strict configuration and construction of the Subagent
// Runtime Plugin.
package factory

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/subagent"
	subagentruntime "github.com/gorenx/goren/subagent/runtime"
)

// Factory constructs the canonical Subagent Runtime Plugin.
type Factory struct {
	runtimeOptions subagentruntime.RuntimeOptions
}

// New constructs a statically linked Factory.
func New(runtimeOptions subagentruntime.RuntimeOptions) *Factory {
	return &Factory{
		runtimeOptions: runtimeOptions,
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
	return subagentruntime.New(builder.runtimeOptions), nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
