// Package factory owns strict configuration and construction of the
// Connection Host Plugin.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	connectionhost "github.com/gorenx/goren/internal/connection"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

// Config is the strict Connection Host deployment configuration.
type Config struct {
	ListenAddress         string   `json:"listenAddress"`
	TrustedHosts          []string `json:"trustedHosts,omitempty"`
	MaxBodyBytes          int64    `json:"maxBodyBytes,omitempty"`
	GracefulTimeoutMillis int64    `json:"gracefulTimeoutMillis,omitempty"`
	ServeWeb              bool     `json:"serveWeb,omitempty"`
}

// Factory constructs the Connection Host Plugin.
type Factory struct{}

// New constructs a statically linked Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Connection Plugin name.
func (*Factory) Name() string {
	return connectionhost.PluginName
}

// Create strictly decodes configuration and constructs the Connection Plugin.
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
	return connectionhost.NewPlugin(
		connectionhost.PluginConfig{
			ListenAddress:  settings.ListenAddress,
			TrustedHosts:   settings.TrustedHosts,
			MaxBodyBytes:   settings.MaxBodyBytes,
			GracefulPeriod: time.Duration(settings.GracefulTimeoutMillis) * time.Millisecond,
			ServeWeb:       settings.ServeWeb,
		},
	)
}

func decodeConfig(rawConfig json.RawMessage) (Config, error) {
	if err := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"connection factory",
	); err != nil {
		return Config{}, err
	}
	var settings Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Config{}, fmt.Errorf(
			"connection factory: decode configuration: %w",
			err,
		)
	}
	if settings.MaxBodyBytes < 0 {
		return Config{}, errors.New(
			"connection factory: maxBodyBytes must not be negative",
		)
	}
	if settings.GracefulTimeoutMillis < 0 {
		return Config{}, errors.New(
			"connection factory: gracefulTimeoutMillis must not be negative",
		)
	}
	maximumTimerMilliseconds := int64((1<<63 - 1) / time.Millisecond)
	if settings.GracefulTimeoutMillis > maximumTimerMilliseconds {
		return Config{}, fmt.Errorf(
			"connection factory: gracefulTimeoutMillis must not exceed %d",
			maximumTimerMilliseconds,
		)
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
