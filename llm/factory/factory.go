// Package factory owns strict configuration and construction of the
// provider-neutral LLM Runtime Plugin.
package factory

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

// Factory constructs the canonical LLM Runtime Plugin.
type Factory struct {
	reporter llm.ObserverFailureReporter
}

// New constructs a statically linked LLM Runtime Factory.
func New(reporter llm.ObserverFailureReporter) *Factory {
	return &Factory{
		reporter: reporter,
	}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return llm.PluginName
}

// Create strictly decodes configuration and constructs the LLM Runtime.
func (builder *Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	if err := pluginfactory.ValidateEmptyConfig(
		rawConfig,
		"llm factory",
	); err != nil {
		return nil, err
	}
	return llm.NewRuntime(builder.reporter), nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
