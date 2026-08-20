// Package factory composes the Workspace Plugin from its domain Registry and
// built-in SQLite storage adapter.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/workspace"
	workspaceSqlite "github.com/gorenx/goren/workspace/persistence/sqlite"
)

// Config owns the built-in Workspace SQLite configuration.
type Config struct {
	Path        string                      `json:"path"`
	JournalMode workspaceSqlite.JournalMode `json:"journalMode,omitempty"`
}

// Factory constructs the canonical Workspace Plugin.
type Factory struct{}

// New constructs a statically linked Workspace Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return workspace.PluginName
}

// Create strictly decodes configuration and constructs the Workspace Plugin.
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
	return workspace.NewRegistry(
		&sqliteBackendOpener{
			settings: workspaceSqlite.Config{
				Path:        settings.Path,
				JournalMode: settings.JournalMode,
			},
		},
		workspace.RegistryOptions{},
	)
}

type sqliteBackendOpener struct {
	settings workspaceSqlite.Config
}

func (opener *sqliteBackendOpener) OpenBackend(
	requestContext context.Context,
) (workspace.Backend, error) {
	return workspaceSqlite.Open(requestContext, opener.settings)
}

func decodeConfig(rawConfig json.RawMessage) (Config, error) {
	if err := jsonvalue.Validate(rawConfig); err != nil {
		return Config{}, fmt.Errorf(
			"workspace factory: invalid configuration: %w",
			err,
		)
	}
	if !jsonvalue.IsObject(rawConfig) {
		return Config{}, errors.New("workspace factory: configuration must be a JSON object")
	}
	var settings Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Config{}, fmt.Errorf("workspace factory: decode configuration: %w", err)
	}
	var trailingValue json.RawMessage
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		return Config{}, errors.New(
			"workspace factory: configuration must contain one JSON value",
		)
	}
	if strings.TrimSpace(settings.Path) == "" {
		return Config{}, errors.New("workspace factory: path must be non-empty")
	}
	switch settings.JournalMode {
	case "":
		settings.JournalMode = workspaceSqlite.JournalWAL
	case workspaceSqlite.JournalWAL, workspaceSqlite.JournalDelete,
		workspaceSqlite.JournalTruncate, workspaceSqlite.JournalPersist:
	default:
		return Config{}, errors.New(
			"workspace factory: journalMode must be wal, delete, truncate, or persist",
		)
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
