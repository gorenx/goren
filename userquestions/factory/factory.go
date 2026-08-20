// Package factory owns strict configuration and construction of the User
// Questions Plugin.
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
	"github.com/gorenx/goren/userquestions"
)

// Config is the intentionally empty User Questions Plugin configuration.
type Config struct{}

// Factory constructs the canonical User Questions Plugin.
type Factory struct{}

// New constructs a statically linked Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return userquestions.PluginName
}

// Create strictly accepts an empty object before constructing the Plugin.
func (*Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := createContext.Err(); err != nil {
		return nil, err
	}
	if err := decodeConfig(rawConfig); err != nil {
		return nil, err
	}
	return userquestions.New(), nil
}

func decodeConfig(rawConfig json.RawMessage) error {
	if err := jsonvalue.Validate(rawConfig); err != nil {
		return fmt.Errorf(
			"userquestions factory: invalid configuration: %w",
			err,
		)
	}
	if !jsonvalue.IsObject(rawConfig) {
		return errors.New(
			"userquestions factory: configuration must be a JSON object",
		)
	}
	var settings Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return fmt.Errorf(
			"userquestions factory: decode configuration: %w",
			err,
		)
	}
	var trailingValue json.RawMessage
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		return errors.New(
			"userquestions factory: configuration must contain one JSON value",
		)
	}
	return nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
