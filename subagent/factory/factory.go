// Package factory owns strict configuration and construction of the Subagent
// Plugin.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/subagent"
	subagentplugin "github.com/gorenx/goren/subagent/plugin"
)

// Config owns the independent Bound Definition database configuration.
type Config struct {
	BoundDefinitions DatabaseConfig `json:"boundDefinitions"`
}

// DatabaseConfig identifies the SQLite database used only for global Bound
// Definitions.
type DatabaseConfig struct {
	Path        string                     `json:"path"`
	JournalMode subagentplugin.JournalMode `json:"journalMode,omitempty"`
}

// Factory constructs the canonical Subagent Plugin.
type Factory struct {
	diagnostics subagentplugin.Diagnostics
}

// New constructs a statically linked Factory.
func New(diagnostics subagentplugin.Diagnostics) *Factory {
	return &Factory{
		diagnostics: diagnostics,
	}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return subagent.PluginName
}

// Create strictly decodes configuration and constructs an inactive Plugin.
func (builder *Factory) Create(
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
	return subagentplugin.New(
		builder.diagnostics,
		subagentplugin.DefinitionDatabase{
			Path:        settings.BoundDefinitions.Path,
			JournalMode: settings.BoundDefinitions.JournalMode,
		},
	), nil
}

func decodeConfig(rawConfig json.RawMessage) (Config, error) {
	if err := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"subagent factory",
	); err != nil {
		return Config{}, err
	}
	var settings Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Config{}, fmt.Errorf(
			"subagent factory: decode configuration: %w",
			err,
		)
	}
	if strings.TrimSpace(settings.BoundDefinitions.Path) == "" {
		return Config{}, errors.New(
			"subagent factory: boundDefinitions.path must be non-empty",
		)
	}
	switch settings.BoundDefinitions.JournalMode {
	case "":
		settings.BoundDefinitions.JournalMode = subagentplugin.JournalWAL
	case subagentplugin.JournalWAL,
		subagentplugin.JournalDelete,
		subagentplugin.JournalTruncate,
		subagentplugin.JournalPersist:
	default:
		return Config{}, errors.New(
			"subagent factory: boundDefinitions.journalMode must be wal, delete, truncate, or persist",
		)
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
