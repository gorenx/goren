// Package factory owns strict /compact Consumer construction.
package factory

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/compaction/command"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

// Factory constructs the canonical /compact Consumer Plugin.
type Factory struct{}

// New constructs a statically linked /compact Consumer Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return command.PluginName
}

// Create validates empty owner-defined configuration.
func (*Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	if err := pluginfactory.ValidateEmptyConfig(
		rawConfig,
		"command-compact factory",
	); err != nil {
		return nil, err
	}
	return command.New(), nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
