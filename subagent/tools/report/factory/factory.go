// Package factory owns strict report Tool configuration and construction.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/subagent/tools/report"
)

// Config selects how accepted reports schedule the direct parent.
type Config struct {
	ReportDelivery report.Delivery `json:"reportDelivery"`
}

// Factory constructs the report Extension Plugin.
type Factory struct{}

// New constructs a statically linked Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Plugin name.
func (*Factory) Name() string {
	return report.PluginName
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
	return report.New(settings.ReportDelivery)
}

func decodeConfig(rawConfig json.RawMessage) (Config, error) {
	if configErr := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"subagent report factory",
	); configErr != nil {
		return Config{}, configErr
	}
	settings := Config{
		ReportDelivery: report.NextStep,
	}
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&settings); decodeErr != nil {
		return Config{}, fmt.Errorf(
			"subagent report factory: decode configuration: %w",
			decodeErr,
		)
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
