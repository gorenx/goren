// Package factory composes the Credentials Plugin from its domain Manager and
// selected storage adapter.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/gorenx/goren/credentials"
	credentialslocal "github.com/gorenx/goren/credentials/local"
	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

// Config selects the Credentials storage adapter and owns its configuration.
type Config struct {
	Local credentialslocal.Config `json:"local"`
}

// Factory constructs the canonical Credentials Plugin.
type Factory struct {
	environment credentials.Environment
}

// New constructs a statically linked Credentials Factory.
func New(environment credentials.Environment) (*Factory, error) {
	if environment == nil {
		return nil, errors.New("credentials factory: environment is required")
	}
	return &Factory{
		environment: environment,
	}, nil
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return credentials.PluginName
}

// Create strictly decodes configuration and composes the Credentials Plugin.
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
	storage, err := credentialslocal.NewLiveStore(settings.Local)
	if err != nil {
		return nil, err
	}
	return credentials.NewManager(storage, builder.environment)
}

func decodeConfig(rawConfig json.RawMessage) (Config, error) {
	if err := jsonvalue.Validate(rawConfig); err != nil {
		return Config{}, fmt.Errorf("credentials factory: invalid configuration: %w", err)
	}
	var settings Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Config{}, fmt.Errorf("credentials factory: decode configuration: %w", err)
	}
	var trailingValue json.RawMessage
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("credentials factory: configuration must contain one JSON value")
	}
	if err := credentialslocal.ValidateConfig(settings.Local); err != nil {
		return Config{}, err
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
