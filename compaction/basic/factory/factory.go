// Package factory owns strict Basic Compaction configuration and construction.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/compaction/basic"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

// Factory constructs the canonical Basic Compaction Provider.
type Factory struct {
	options basic.RuntimeOptions
}

// New constructs a statically linked Factory.
func New(options basic.RuntimeOptions) *Factory {
	return &Factory{
		options: options,
	}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return basic.PluginName
}

// Create strictly decodes and validates Provider configuration.
func (builder *Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	if err := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"compaction-basic factory",
	); err != nil {
		return nil, err
	}
	var settings basic.Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return nil, fmt.Errorf(
			"compaction-basic factory: decode configuration: %w",
			err,
		)
	}
	resolved, err := basic.ResolveConfig(settings)
	if err != nil {
		return nil, err
	}
	return basic.New(resolved, builder.options), nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
