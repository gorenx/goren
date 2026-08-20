// Package factory owns strict configuration and construction of the API Proxy
// Host Plugin.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/gorenx/goren/apiproxy"
	apiproxyhost "github.com/gorenx/goren/apiproxy/host"
	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

// Config is the strict deployment configuration owned by the API Proxy
// Factory.
type Config struct {
	Version string `json:"version"`
}

// Factory constructs the canonical API Proxy Plugin.
type Factory struct {
	runtimeOptions apiproxyhost.RuntimeOptions
}

// New constructs a statically linked Factory.
func New(runtimeOptions apiproxyhost.RuntimeOptions) *Factory {
	return &Factory{
		runtimeOptions: runtimeOptions,
	}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return apiproxy.PluginName
}

// Create strictly decodes configuration before constructing the Host Plugin.
func (builder *Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := createContext.Err(); err != nil {
		return nil, err
	}
	settings, err := decodeConfig(rawConfig)
	if err != nil {
		return nil, err
	}
	return apiproxyhost.New(
		apiproxyhost.Settings{
			Version: settings.Version,
		},
		builder.runtimeOptions,
	)
}

func decodeConfig(rawConfig json.RawMessage) (Config, error) {
	if err := jsonvalue.Validate(rawConfig); err != nil {
		return Config{}, fmt.Errorf(
			"apiproxy factory: invalid configuration: %w",
			err,
		)
	}
	if !jsonvalue.IsObject(rawConfig) {
		return Config{}, errors.New(
			"apiproxy factory: configuration must be a JSON object",
		)
	}
	var settings Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Config{}, fmt.Errorf(
			"apiproxy factory: decode configuration: %w",
			err,
		)
	}
	var trailingValue json.RawMessage
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		return Config{}, errors.New(
			"apiproxy factory: configuration must contain one JSON value",
		)
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
