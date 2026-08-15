package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sessionpersistence "github.com/gorenx/goren/session/persistence"
	sqlitepersistence "github.com/gorenx/goren/session/persistence/sqlite"
)

// SessionPersistenceSQLiteConfig is the strict deployment configuration for one fact store.
type SessionPersistenceSQLiteConfig struct {
	Path                 string                        `json:"path"`
	JournalMode          sqlitepersistence.JournalMode `json:"journalMode,omitempty"`
	WriteBatchMaxDelayMS int64                         `json:"writeBatchMaxDelayMs,omitempty"`
}

type sessionPersistenceSQLiteFactory struct{}

func (sessionPersistenceSQLiteFactory) Name() string { return SessionPersistenceSQLiteFactoryName }

func (sessionPersistenceSQLiteFactory) DecodeConfig(
	rawConfig json.RawMessage,
) (SessionPersistenceSQLiteConfig, error) {
	settings, err := plugin.DecodeStrictConfig(rawConfig, func(candidate SessionPersistenceSQLiteConfig) error {
		if strings.TrimSpace(candidate.Path) == "" {
			return errors.New("path must be non-empty")
		}
		switch candidate.JournalMode {
		case "", sqlitepersistence.JournalWAL, sqlitepersistence.JournalDelete,
			sqlitepersistence.JournalTruncate, sqlitepersistence.JournalPersist:
		default:
			return errors.New("journalMode must be wal, delete, truncate, or persist")
		}
		if candidate.WriteBatchMaxDelayMS < 0 {
			return errors.New("writeBatchMaxDelayMs must be a positive integer")
		}
		return nil
	})
	if err != nil {
		return SessionPersistenceSQLiteConfig{}, err
	}
	if settings.JournalMode == "" {
		settings.JournalMode = sqlitepersistence.JournalWAL
	}
	if settings.WriteBatchMaxDelayMS == 0 {
		settings.WriteBatchMaxDelayMS = sessionpersistence.DefaultWriteBatchMaxDelay.Milliseconds()
	}
	return settings, nil
}

func (sessionPersistenceSQLiteFactory) New(
	_ context.Context,
	settings SessionPersistenceSQLiteConfig,
) (plugin.Plugin, error) {
	return &sessionPersistenceSQLitePlugin{settings: settings}, nil
}

type sessionPersistenceSQLitePlugin struct {
	settings SessionPersistenceSQLiteConfig
}

func (*sessionPersistenceSQLitePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:     SessionPersistenceSQLiteFactoryName,
		Provides: []plugin.ServiceRef{sessionpersistence.Service.Ref()},
		Requires: []plugin.ServiceRef{session.StoreService.Ref()},
	}
}

func (instance *sessionPersistenceSQLitePlugin) Apply(
	requestContext context.Context,
	pluginScope *plugin.Scope,
) error {
	store, found := plugin.Require(pluginScope, session.StoreService)
	if !found {
		return errors.New("assembly: Session Persistence SQLite dependency is unavailable")
	}
	storage, err := sqlitepersistence.Open(requestContext, sqlitepersistence.Config{
		Path: instance.settings.Path, JournalMode: instance.settings.JournalMode,
	})
	if err != nil {
		return err
	}
	durability, err := sessionpersistence.NewCoordinator(
		requestContext, pluginScope, store, storage,
		sessionpersistence.CoordinatorOptions{
			WriteBatchMaxDelay: time.Duration(instance.settings.WriteBatchMaxDelayMS) * time.Millisecond,
		},
	)
	if err != nil {
		_ = storage.Close(requestContext)
		return err
	}
	_, err = plugin.Provide(pluginScope, sessionpersistence.Service, sessionpersistence.Persistence(durability))
	return err
}
