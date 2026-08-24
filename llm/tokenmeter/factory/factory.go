// Package factory owns strict Token Meter construction.
package factory

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

// Factory constructs the canonical Token Meter Plugin.
type Factory struct{}

// New constructs a statically linked Token Meter Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return tokenmeter.PluginName
}

// Create strictly accepts the source-compatible empty configuration.
func (*Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	if err := pluginfactory.ValidateEmptyConfig(
		rawConfig,
		"tokenmeter factory",
	); err != nil {
		return nil, err
	}
	return tokenmeter.New(), nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
