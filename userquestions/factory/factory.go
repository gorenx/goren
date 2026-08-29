// Package factory owns strict configuration and construction of the User
// Questions Plugin.
package factory

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/userquestions"
)

// Factory constructs the canonical User Questions Plugin.
type Factory struct{}

// New constructs a statically linked Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return userquestions.PluginName
}

// Create strictly accepts an empty object before constructing the Plugin.
func (*Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	if err := pluginfactory.ValidateEmptyConfig(
		rawConfig,
		"userquestions factory",
	); err != nil {
		return nil, err
	}
	return userquestions.NewPlugin(), nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
