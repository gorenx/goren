// Package factory owns strict configuration and construction of the Session
// Projection Registry Plugin.
package factory

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/session/projection"
)

// Factory constructs the canonical Session Projection Registry Plugin.
type Factory struct{}

// New constructs a statically linked Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return projection.PluginName
}

// Create strictly decodes configuration and constructs the Registry Plugin.
func (*Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	if err := pluginfactory.ValidateEmptyConfig(
		rawConfig,
		"session projection factory",
	); err != nil {
		return nil, err
	}
	return projection.NewDriveRegistry(), nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
