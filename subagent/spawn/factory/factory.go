// Package factory owns strict spawn Plugin configuration and construction.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/subagent/spawn"
)

// Config preserves the canonical providerName key while selecting the exact
// SeedBuilder registry name.
type Config struct {
	ProviderName string `json:"providerName"`
}

// Factory constructs the spawn SeedBuilder Plugin.
type Factory struct{}

// New constructs a statically linked Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Plugin name.
func (*Factory) Name() string {
	return spawn.PluginName
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
	return spawn.NewPlugin(settings.ProviderName)
}

func decodeConfig(rawConfig json.RawMessage) (Config, error) {
	if configErr := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"subagent spawn factory",
	); configErr != nil {
		return Config{}, configErr
	}
	settings := Config{
		ProviderName: spawn.DefaultSeedBuilderName,
	}
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&settings); decodeErr != nil {
		return Config{}, fmt.Errorf(
			"subagent spawn factory: decode configuration: %w",
			decodeErr,
		)
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
