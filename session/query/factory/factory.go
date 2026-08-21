// Package factory composes the Session Query Plugin from its domain Service
// and built-in disposable SQLite index.
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
	"github.com/gorenx/goren/session/query"
	querysqlite "github.com/gorenx/goren/session/query/sqlite"
)

// Config owns Session Query policy and built-in SQLite index configuration.
type Config struct {
	Path                        string                  `json:"path"`
	JournalMode                 querysqlite.JournalMode `json:"journalMode,omitempty"`
	DefaultLimit                int                     `json:"defaultLimit,omitempty"`
	MaximumLimit                int                     `json:"maximumLimit,omitempty"`
	SnippetCodePoints           int                     `json:"snippetCodePoints,omitempty"`
	ReadWindowMax               *int                    `json:"readWindowMax,omitempty"`
	PersistedInspectConcurrency int                     `json:"persistedInspectConcurrency,omitempty"`
}

// Factory constructs the canonical Session Query Plugin.
type Factory struct{}

// New constructs a statically linked Session Query Factory.
func New() *Factory {
	return &Factory{}
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return query.PluginName
}

// Create strictly decodes configuration and constructs the Session Query Plugin.
func (*Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (plugin.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	settings, querySettings, err := decodeConfig(rawConfig)
	if err != nil {
		return nil, err
	}
	return query.New(
		&sqliteIndexOpener{
			settings: querysqlite.Config{
				Path:              settings.Path,
				JournalMode:       settings.JournalMode,
				SnippetCodePoints: settings.SnippetCodePoints,
			},
		},
		querySettings,
	)
}

type sqliteIndexOpener struct {
	settings querysqlite.Config
}

func (opener *sqliteIndexOpener) OpenIndex(
	requestContext context.Context,
) (query.Index, error) {
	return querysqlite.Open(requestContext, opener.settings)
}

func decodeConfig(rawConfig json.RawMessage) (Config, query.Config, error) {
	if err := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"session query factory",
	); err != nil {
		return Config{}, query.Config{}, err
	}
	var settings Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Config{}, query.Config{}, fmt.Errorf(
			"session query factory: decode configuration: %w",
			err,
		)
	}
	if strings.TrimSpace(settings.Path) == "" {
		return Config{}, query.Config{}, errors.New(
			"session query factory: path must be non-empty",
		)
	}
	switch settings.JournalMode {
	case "":
		settings.JournalMode = querysqlite.JournalWAL
	case querysqlite.JournalWAL, querysqlite.JournalDelete,
		querysqlite.JournalTruncate, querysqlite.JournalPersist:
	default:
		return Config{}, query.Config{}, errors.New(
			"session query factory: journalMode must be wal, delete, truncate, or persist",
		)
	}
	if settings.SnippetCodePoints < 0 {
		return Config{}, query.Config{}, errors.New(
			"session query factory: snippetCodePoints must be positive",
		)
	}
	querySettings, err := query.ValidateConfig(
		query.Config{
			DefaultLimit:                settings.DefaultLimit,
			MaximumLimit:                settings.MaximumLimit,
			ReadWindowMax:               settings.ReadWindowMax,
			PersistedInspectConcurrency: settings.PersistedInspectConcurrency,
		},
	)
	if err != nil {
		return Config{}, query.Config{}, err
	}
	return settings, querySettings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
