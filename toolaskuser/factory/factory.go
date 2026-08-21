// Package factory owns strict configuration and construction of the
// ask_user_question Tool Plugin.
package factory

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/toolaskuser"
)

// Factory constructs the canonical ask-user Tool Plugin.
type Factory struct{}

// New constructs a statically linked Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return toolaskuser.PluginName
}

// Create strictly decodes configuration and constructs the Tool Plugin.
func (*Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	if err := pluginfactory.ValidateEmptyConfig(
		rawConfig,
		"toolaskuser factory",
	); err != nil {
		return nil, err
	}
	return toolaskuser.New(), nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
