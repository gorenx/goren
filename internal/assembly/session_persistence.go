package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sesssqlite "github.com/gorenx/goren/session/persistence/sqlite"
)

// SessionPersistenceConfig configures the durable Session capability. SQLite
// is the built-in Backend adapter, not a separately loadable plugin.
type SessionPersistenceConfig struct {
	Path                 string                 `json:"path"`
	JournalMode          sesssqlite.JournalMode `json:"journalMode,omitempty"`
	WriteBatchMaxDelayMS int64                  `json:"writeBatchMaxDelayMs,omitempty"`
}

type sessionPersistenceFactory struct{}

func (sessionPersistenceFactory) Name() string { return SessionPersistenceFactoryName }

func (sessionPersistenceFactory) DecodeConfig(
	rawConfig json.RawMessage,
) (SessionPersistenceConfig, error) {
	settings, err := plugin.DecodeStrictConfig(rawConfig, func(candidate SessionPersistenceConfig) error {
		if strings.TrimSpace(candidate.Path) == "" {
			return errors.New("path must be non-empty")
		}
		switch candidate.JournalMode {
		case "", sesssqlite.JournalWAL, sesssqlite.JournalDelete,
			sesssqlite.JournalTruncate, sesssqlite.JournalPersist:
		default:
			return errors.New("journalMode must be wal, delete, truncate, or persist")
		}
		if candidate.WriteBatchMaxDelayMS < 0 {
			return errors.New("writeBatchMaxDelayMs must be a positive integer")
		}
		return nil
	})
	if err != nil {
		return SessionPersistenceConfig{}, err
	}
	if settings.JournalMode == "" {
		settings.JournalMode = sesssqlite.JournalWAL
	}
	if settings.WriteBatchMaxDelayMS == 0 {
		settings.WriteBatchMaxDelayMS = sesspersist.DefaultWriteBatchMaxDelay.Milliseconds()
	}
	return settings, nil
}

func (sessionPersistenceFactory) New(
	_ context.Context,
	settings SessionPersistenceConfig,
) (plugin.Plugin, error) {
	return &sessionPersistencePlugin{settings: settings}, nil
}

type sessionPersistencePlugin struct {
	settings SessionPersistenceConfig
}

func (*sessionPersistencePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:     SessionPersistenceFactoryName,
		Provides: []plugin.ServiceRef{sesspersist.Service.Ref()},
		Requires: []plugin.ServiceRef{session.StoreService.Ref()},
	}
}

func (instance *sessionPersistencePlugin) Apply(
	requestContext context.Context,
	pluginScope *plugin.Scope,
) error {
	store, found := plugin.Require(pluginScope, session.StoreService)
	if !found {
		return errors.New("assembly: Session Persistence dependency is unavailable")
	}
	storage, err := sesssqlite.Open(requestContext, sesssqlite.Config{
		Path: instance.settings.Path, JournalMode: instance.settings.JournalMode,
	})
	if err != nil {
		return err
	}
	durability, err := sesspersist.NewSessionLogStore(
		requestContext, pluginScope, store, storage,
		sesspersist.SessionLogStoreOptions{
			WriteBatchMaxDelay: time.Duration(instance.settings.WriteBatchMaxDelayMS) * time.Millisecond,
		},
	)
	if err != nil {
		_ = storage.Close(requestContext)
		return err
	}
	_, err = plugin.Provide(pluginScope, sesspersist.Service, sesspersist.Persistence(durability))
	return err
}
