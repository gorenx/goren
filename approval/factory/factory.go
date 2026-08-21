// Package factory owns strict configuration and construction of the Approval
// Plugin.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

// Factory constructs the canonical root Approval Plugin.
type Factory struct{}

// New constructs a statically linked Approval Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return approval.PluginName
}

// Create strictly decodes configuration and constructs the root Approval
// Service. Runtime dependencies are resolved later during Apply.
func (*Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	settings, err := decodeConfig(rawConfig)
	if err != nil {
		return nil, err
	}
	validated, err := approval.ValidateConfig(settings)
	if err != nil {
		return nil, err
	}
	return approval.New(validated), nil
}

func decodeConfig(rawConfig json.RawMessage) (approval.Config, error) {
	if err := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"approval factory",
	); err != nil {
		return approval.Config{}, err
	}
	var settings approval.Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	if err := decoder.Decode(&settings); err != nil {
		return approval.Config{}, fmt.Errorf(
			"approval factory: decode configuration: %w",
			err,
		)
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
