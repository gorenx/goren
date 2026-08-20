// Package factory owns strict configuration and construction of the
// provider-neutral LLM Runtime Plugin.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

// Config is the provider-neutral LLM Runtime deployment configuration.
type Config struct{}

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
	if err := createContext.Err(); err != nil {
		return nil, err
	}
	if _, err := decodeConfig(rawConfig); err != nil {
		return nil, err
	}
	return llm.NewRuntime(builder.reporter), nil
}

func decodeConfig(rawConfig json.RawMessage) (Config, error) {
	if err := jsonvalue.Validate(rawConfig); err != nil {
		return Config{}, fmt.Errorf("llm factory: invalid configuration: %w", err)
	}
	if !jsonvalue.IsObject(rawConfig) {
		return Config{}, errors.New("llm factory: configuration must be a JSON object")
	}
	var settings Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Config{}, fmt.Errorf("llm factory: decode configuration: %w", err)
	}
	var trailingValue json.RawMessage
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("llm factory: configuration must contain one JSON value")
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
