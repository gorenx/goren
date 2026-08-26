// Package factory composes the Session Projection Cache Plugin with its built-in
// SQLite checkpoint Store.
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pluginruntime "github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/session/projectioncache"
	cacheplugin "github.com/gorenx/goren/session/projectioncache/plugin"
	cachesqlite "github.com/gorenx/goren/session/projectioncache/sqlite"
)

// Config owns checkpoint scheduling and built-in SQLite configuration.
type Config struct {
	Path             string                  `json:"path"`
	JournalMode      cachesqlite.JournalMode `json:"journalMode,omitempty"`
	WriteEveryEvents int                     `json:"writeEveryEvents,omitempty"`
	WriteIntervalMS  int64                   `json:"writeIntervalMs,omitempty"`
}

// Factory constructs the canonical Session Projection Cache Plugin.
type Factory struct {
	failures projectioncache.FailureReporter
}

// New constructs a statically linked Projection Cache Factory.
func New(failures projectioncache.FailureReporter) (*Factory, error) {
	if failures == nil {
		return nil, errors.New(
			"session projection cache factory: failure reporter is required",
		)
	}
	return &Factory{
		failures: failures,
	}, nil
}

// Name returns the canonical Harness Plugin name.
func (*Factory) Name() string {
	return projectioncache.PluginName
}

// Create strictly decodes configuration and constructs the inactive Plugin.
func (builder *Factory) Create(
	createContext context.Context,
	rawConfig json.RawMessage,
) (pluginruntime.Plugin, error) {
	if err := pluginfactory.ValidateCreateContext(createContext); err != nil {
		return nil, err
	}
	settings, err := decodeConfig(rawConfig)
	if err != nil {
		return nil, err
	}
	return cacheplugin.New(
		&sqliteStoreOpener{
			settings: cachesqlite.Config{
				Path:        settings.Path,
				JournalMode: settings.JournalMode,
			},
		},
		projectioncache.Config{
			WriteEveryEvents: settings.WriteEveryEvents,
			WriteInterval: time.Duration(settings.WriteIntervalMS) *
				time.Millisecond,
			Failures: builder.failures,
		},
	)
}

type sqliteStoreOpener struct {
	settings cachesqlite.Config
}

func (opener *sqliteStoreOpener) OpenCheckpointStore(
	requestContext context.Context,
) (projectioncache.CheckpointStore, error) {
	return cachesqlite.Open(requestContext, opener.settings)
}

func decodeConfig(rawConfig json.RawMessage) (Config, error) {
	if err := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"session projection cache factory",
	); err != nil {
		return Config{}, err
	}
	var settings Config
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Config{}, fmt.Errorf(
			"session projection cache factory: decode configuration: %w",
			err,
		)
	}
	if strings.TrimSpace(settings.Path) == "" {
		return Config{}, errors.New(
			"session projection cache factory: path must be non-empty",
		)
	}
	switch settings.JournalMode {
	case "":
		settings.JournalMode = cachesqlite.JournalWAL
	case cachesqlite.JournalWAL,
		cachesqlite.JournalDelete,
		cachesqlite.JournalTruncate,
		cachesqlite.JournalPersist:
	default:
		return Config{}, errors.New(
			"session projection cache factory: journalMode must be wal, delete, truncate, or persist",
		)
	}
	if settings.WriteEveryEvents < 0 {
		return Config{}, errors.New(
			"session projection cache factory: writeEveryEvents must be a positive integer",
		)
	}
	if settings.WriteEveryEvents == 0 {
		settings.WriteEveryEvents = projectioncache.DefaultWriteEveryEvents
	}
	if settings.WriteIntervalMS < 0 {
		return Config{}, errors.New(
			"session projection cache factory: writeIntervalMs must be a positive integer",
		)
	}
	maximumTimerMilliseconds := int64((1<<63 - 1) / time.Millisecond)
	if settings.WriteIntervalMS > maximumTimerMilliseconds {
		return Config{}, fmt.Errorf(
			"session projection cache factory: writeIntervalMs must not exceed %d",
			maximumTimerMilliseconds,
		)
	}
	if settings.WriteIntervalMS == 0 {
		settings.WriteIntervalMS = projectioncache.DefaultWriteInterval.Milliseconds()
	}
	return settings, nil
}

var _ pluginfactory.Factory = (*Factory)(nil)
