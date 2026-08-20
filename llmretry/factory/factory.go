// Package factory owns strict configuration and construction of the LLM Retry
// Plugin.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/gorenx/goren/internal/jsonvalue"
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
	if err := createContext.Err(); err != nil {
		return nil, err
	}
	if err := decodeConfig(rawConfig); err != nil {
		return nil, err
	}
	return llmretry.New(builder.runtimeOptions), nil
}

func decodeConfig(rawConfig json.RawMessage) error {
	if err := jsonvalue.Validate(rawConfig); err != nil {
		return fmt.Errorf(
			"llmretry factory: invalid configuration: %w",
			err,
		)
	}
	if !jsonvalue.IsObject(rawConfig) {
		return errors.New(
			"llmretry factory: configuration must be a JSON object",
		)
	}
	var settings llmretry.Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	if err := decoder.Decode(&settings); err != nil {
		return fmt.Errorf(
			"llmretry factory: decode configuration: %w",
			err,
		)
	}
	var trailingValue json.RawMessage
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		return errors.New(
			"llmretry factory: configuration must contain one JSON value",
		)
	}
	return nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
