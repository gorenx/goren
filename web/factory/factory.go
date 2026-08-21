// Package factory owns strict configuration and construction of the embedded
// Web frontend Plugin.
package factory

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/web"
)

// Factory constructs the embedded frontend Plugin.
type Factory struct{}

// New constructs a statically linked Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical frontend Plugin name.
func (*Factory) Name() string {
	return web.PluginName
}

// Create strictly decodes configuration and constructs the frontend Plugin.
func (*Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	if err := pluginfactory.ValidateEmptyConfig(
		rawConfig,
		"web factory",
	); err != nil {
		return nil, err
	}
	return web.New(), nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
