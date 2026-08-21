// Package factory owns strict configuration and construction of the LLM Retry
// Plugin.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/llmretry"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

// Factory constructs the canonical LLM Retry Plugin.
type Factory struct {
	runtimeOptions llmretry.RuntimeOptions
}

// New constructs a statically linked Factory.
func New(runtimeOptions llmretry.RuntimeOptions) *Factory {
	return &Factory{
		runtimeOptions: runtimeOptions,
	}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return llmretry.PluginName
}

// Create strictly decodes the empty executor config before construction.
func (builder *Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	if err := decodeConfig(rawConfig); err != nil {
		return nil, err
	}
	return llmretry.New(builder.runtimeOptions), nil
}

func decodeConfig(rawConfig json.RawMessage) error {
	if err := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"llmretry factory",
	); err != nil {
		return err
	}
	var settings llmretry.Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	if err := decoder.Decode(&settings); err != nil {
		return fmt.Errorf(
			"llmretry factory: decode configuration: %w",
			err,
		)
	}
	return nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
