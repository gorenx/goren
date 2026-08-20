// Package factory owns strict configuration and construction of the Session
// Title Plugin.
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
	"github.com/gorenx/goren/session/title"
)

// Factory constructs the canonical Session Title Plugin.
type Factory struct {
	reporter title.AsyncFailureReporter
}

// New constructs a statically linked Session Title Factory.
func New(reporter title.AsyncFailureReporter) (*Factory, error) {
	if reporter == nil {
		return nil, errors.New(
			"session title factory: asynchronous failure reporter is required",
		)
	}
	return &Factory{
		reporter: reporter,
	}, nil
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return title.PluginName
}

// Create strictly decodes configuration and constructs the Session Title Plugin.
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
	return title.NewLogService(settings, builder.reporter)
}

func decodeConfig(rawConfig json.RawMessage) (title.Config, error) {
	if err := jsonvalue.Validate(rawConfig); err != nil {
		return title.Config{}, fmt.Errorf(
			"session title factory: invalid configuration: %w",
			err,
		)
	}
	if !jsonvalue.IsObject(rawConfig) {
		return title.Config{}, errors.New(
			"session title factory: configuration must be a JSON object",
		)
	}
	var settings title.Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return title.Config{}, fmt.Errorf(
			"session title factory: decode configuration: %w",
			err,
		)
	}
	var trailingValue json.RawMessage
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		return title.Config{}, errors.New(
			"session title factory: configuration must contain one JSON value",
		)
	}
	validated, err := settings.Validate()
	if err != nil {
		return title.Config{}, err
	}
	return validated, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
