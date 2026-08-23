// Package factory owns strict delegation Tool configuration and construction.
package factory

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/subagent/tool"
)

// Factory constructs one provider-bound delegation Tool Plugin.
type Factory struct{}

// New constructs a statically linked Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Plugin name.
func (*Factory) Name() string {
	return tool.PluginName
}

// Create strictly decodes configuration and constructs the Plugin.
func (*Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if contextErr := pluginfactory.ValidateCreateContext(createContext); contextErr != nil {
		return nil, contextErr
	}
	settings, decodeErr := decodeConfig(rawConfig)
	if decodeErr != nil {
		return nil, decodeErr
	}
	return tool.New(settings)
}

var _ pluginfactory.Factory = (*Factory)(nil)
