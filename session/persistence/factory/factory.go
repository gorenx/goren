// Package factory composes the durable Session Plugin from its domain Service
// and built-in SQLite storage adapter.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/session/persistence"
	sesssqlite "github.com/gorenx/goren/session/persistence/sqlite"
)

// Config owns durable Session policy and built-in SQLite configuration.
type Config struct {
	Path                     string                 `json:"path"`
	JournalMode              sesssqlite.JournalMode `json:"journalMode,omitempty"`
	WriteBatchMaxDelayMS     int64                  `json:"writeBatchMaxDelayMs,omitempty"`
	PreparedSessionCacheSize int                    `json:"preparedSessionCacheSize,omitempty"`
}

// Factory constructs the canonical durable Session Plugin.
type Factory struct {
	backgroundWriteFailures persistence.BackgroundWriteFailureReporter
}

// New constructs a statically linked Session Persistence Factory.
func New(
	backgroundWriteFailures persistence.BackgroundWriteFailureReporter,
) (*Factory, error) {
	if backgroundWriteFailures == nil {
		return nil, errors.New(
			"session persistence factory: background write failure reporter is required",
		)
	}
	return &Factory{
		backgroundWriteFailures: backgroundWriteFailures,
	}, nil
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return persistence.PluginName
}

// Create strictly decodes configuration and constructs the durable Session Plugin.
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
	return persistence.NewSessionLogStore(
		&sqliteBackendOpener{
			settings: sesssqlite.Config{
				Path:        settings.Path,
				JournalMode: settings.JournalMode,
			},
		},
		persistence.SessionLogStoreOptions{
			WriteBatchMaxDelay: time.Duration(settings.WriteBatchMaxDelayMS) *
				time.Millisecond,
			PreparedSessionCacheSize: settings.PreparedSessionCacheSize,
			BackgroundWriteFailures:  builder.backgroundWriteFailures,
		},
	)
}

type sqliteBackendOpener struct {
	settings sesssqlite.Config
}

func (opener *sqliteBackendOpener) OpenBackend(
	requestContext context.Context,
) (persistence.Backend, error) {
	return sesssqlite.Open(requestContext, opener.settings)
}

func decodeConfig(rawConfig json.RawMessage) (Config, error) {
	if err := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"session persistence factory",
	); err != nil {
		return Config{}, err
	}
	var settings Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Config{}, fmt.Errorf(
			"session persistence factory: decode configuration: %w",
			err,
		)
	}
	if strings.TrimSpace(settings.Path) == "" {
		return Config{}, errors.New("session persistence factory: path must be non-empty")
	}
	switch settings.JournalMode {
	case "":
		settings.JournalMode = sesssqlite.JournalWAL
	case sesssqlite.JournalWAL, sesssqlite.JournalDelete,
		sesssqlite.JournalTruncate, sesssqlite.JournalPersist:
	default:
		return Config{}, errors.New(
			"session persistence factory: journalMode must be wal, delete, truncate, or persist",
		)
	}
	if settings.WriteBatchMaxDelayMS < 0 {
		return Config{}, errors.New(
			"session persistence factory: writeBatchMaxDelayMs must be a positive integer",
		)
	}
	maximumTimerMilliseconds := int64((1<<63 - 1) / time.Millisecond)
	if settings.WriteBatchMaxDelayMS > maximumTimerMilliseconds {
		return Config{}, fmt.Errorf(
			"session persistence factory: writeBatchMaxDelayMs must not exceed %d",
			maximumTimerMilliseconds,
		)
	}
	if settings.WriteBatchMaxDelayMS == 0 {
		settings.WriteBatchMaxDelayMS = persistence.DefaultWriteBatchMaxDelay.Milliseconds()
	}
	if settings.PreparedSessionCacheSize < 0 {
		return Config{}, errors.New(
			"session persistence factory: preparedSessionCacheSize must be a positive integer",
		)
	}
	if settings.PreparedSessionCacheSize == 0 {
		settings.PreparedSessionCacheSize = persistence.DefaultPreparedSessionCache
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
