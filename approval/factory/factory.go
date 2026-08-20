// Package factory owns strict configuration and construction of the Approval
// Plugin.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/internal/jsonvalue"
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
	if err := createContext.Err(); err != nil {
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
	if err := jsonvalue.Validate(rawConfig); err != nil {
		return approval.Config{}, fmt.Errorf(
			"approval factory: invalid configuration: %w",
			err,
		)
	}
	if !jsonvalue.IsObject(rawConfig) {
		return approval.Config{}, errors.New(
			"approval factory: configuration must be a JSON object",
		)
	}
	var settings approval.Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	if err := decoder.Decode(&settings); err != nil {
		return approval.Config{}, fmt.Errorf(
			"approval factory: decode configuration: %w",
			err,
		)
	}
	var trailingValue json.RawMessage
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		return approval.Config{}, errors.New(
			"approval factory: configuration must contain one JSON value",
		)
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
