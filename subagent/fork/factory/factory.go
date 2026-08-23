// Package factory owns strict fork Provider configuration and construction.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/subagent/fork"
)

// Config selects the exact Provider registry name.
type Config struct {
	ProviderName string `json:"providerName"`
}

// Factory constructs the fork Provider Plugin.
type Factory struct{}

// New constructs a statically linked Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Plugin name.
func (*Factory) Name() string {
	return fork.PluginName
}

// Create strictly decodes configuration and constructs the Provider.
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
	return fork.New(settings.ProviderName)
}

func decodeConfig(rawConfig json.RawMessage) (Config, error) {
	if configErr := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"subagent fork factory",
	); configErr != nil {
		return Config{}, configErr
	}
	settings := Config{
		ProviderName: fork.DefaultProviderName,
	}
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&settings); decodeErr != nil {
		return Config{}, fmt.Errorf(
			"subagent fork factory: decode configuration: %w",
			decodeErr,
		)
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
