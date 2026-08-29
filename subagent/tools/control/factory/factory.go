// Package factory constructs the Subagent control Tool Plugin.
package factory

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/subagent/tools/control"
)

// Factory constructs the control Tool Plugin.
type Factory struct{}

// New constructs a statically linked Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Plugin name.
func (*Factory) Name() string {
	return control.PluginName
}

// Create validates empty configuration and constructs the Plugin.
func (*Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if contextErr := pluginfactory.ValidateCreateContext(createContext); contextErr != nil {
		return nil, contextErr
	}
	if configErr := pluginfactory.ValidateEmptyConfig(
		rawConfig,
		"subagent control factory",
	); configErr != nil {
		return nil, configErr
	}
	return control.New(), nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
