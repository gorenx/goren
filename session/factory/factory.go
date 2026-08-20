// Package factory owns strict configuration and construction of the Session
// Store Plugin.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/session"
)

// Config is the Session Store deployment configuration.
type Config struct{}

// Factory constructs the canonical Session Store Plugin.
type Factory struct {
	postCommitFailures session.PostCommitFailureReporter
}

// New constructs a statically linked Session Factory.
func New(postCommitFailures session.PostCommitFailureReporter) (*Factory, error) {
	if postCommitFailures == nil {
		return nil, errors.New("session factory: post-commit failure reporter is required")
	}
	return &Factory{
		postCommitFailures: postCommitFailures,
	}, nil
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return session.PluginName
}

// Create strictly decodes configuration and constructs the Session Store.
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
	return session.NewMemoryStore(
		session.MemoryStoreOptions{
			PostCommitFailures: builder.postCommitFailures,
		},
	)
}

func decodeConfig(rawConfig json.RawMessage) (Config, error) {
	if err := jsonvalue.Validate(rawConfig); err != nil {
		return Config{}, fmt.Errorf("session factory: invalid configuration: %w", err)
	}
	if !jsonvalue.IsObject(rawConfig) {
		return Config{}, errors.New("session factory: configuration must be a JSON object")
	}
	var settings Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Config{}, fmt.Errorf("session factory: decode configuration: %w", err)
	}
	var trailingValue json.RawMessage
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("session factory: configuration must contain one JSON value")
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
