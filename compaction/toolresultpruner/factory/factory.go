// Package factory owns strict Tool Result Pruner construction.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/compaction/toolresultpruner"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

// Factory constructs the canonical Tool Result Pruner Plugin.
type Factory struct{}

// New constructs a statically linked Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return toolresultpruner.PluginName
}

// Create strictly decodes and validates pruning configuration.
func (*Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	if err := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"toolresultpruner factory",
	); err != nil {
		return nil, err
	}
	var settings toolresultpruner.Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return nil, fmt.Errorf(
			"toolresultpruner factory: decode configuration: %w",
			err,
		)
	}
	resolved, err := toolresultpruner.ResolveConfig(settings)
	if err != nil {
		return nil, err
	}
	return toolresultpruner.New(resolved), nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
