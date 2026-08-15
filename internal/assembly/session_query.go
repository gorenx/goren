package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sessionquery "github.com/gorenx/goren/session/query"
	querysqlite "github.com/gorenx/goren/session/query/sqlite"
)

// SessionQueryConfig configures the Session Query domain service and its
// built-in disposable SQLite index. SQLite is an adapter, not another plugin.
type SessionQueryConfig struct {
	Path                        string                  `json:"path"`
	JournalMode                 querysqlite.JournalMode `json:"journalMode,omitempty"`
	DefaultLimit                int                     `json:"defaultLimit,omitempty"`
	MaximumLimit                int                     `json:"maximumLimit,omitempty"`
	SnippetCodePoints           int                     `json:"snippetCodePoints,omitempty"`
	ReadWindowMax               *int                    `json:"readWindowMax,omitempty"`
	PersistedInspectConcurrency int                     `json:"persistedInspectConcurrency,omitempty"`
}

type sessionQueryFactory struct{}

func (sessionQueryFactory) Name() string { return SessionQueryFactoryName }

func (sessionQueryFactory) DecodeConfig(rawConfig json.RawMessage) (SessionQueryConfig, error) {
	return plugin.DecodeStrictConfig(rawConfig, func(settings SessionQueryConfig) error {
		if strings.TrimSpace(settings.Path) == "" {
			return errors.New("path must be non-empty")
		}
		switch settings.JournalMode {
		case "", querysqlite.JournalWAL, querysqlite.JournalDelete,
			querysqlite.JournalTruncate, querysqlite.JournalPersist:
		default:
			return errors.New("journalMode must be wal, delete, truncate, or persist")
		}
		_, err := sessionquery.ValidateConfig(sessionquery.Config{
			DefaultLimit: settings.DefaultLimit, MaximumLimit: settings.MaximumLimit,
			ReadWindowMax:               settings.ReadWindowMax,
			PersistedInspectConcurrency: settings.PersistedInspectConcurrency,
		})
		if err != nil {
			return err
		}
		if settings.SnippetCodePoints < 0 {
			return errors.New("snippetCodePoints must be positive")
		}
		return nil
	})
}

func (sessionQueryFactory) New(_ context.Context, settings SessionQueryConfig) (plugin.Plugin, error) {
	return &sessionQueryPlugin{settings: settings}, nil
}

type sessionQueryPlugin struct {
	settings SessionQueryConfig
}

func (*sessionQueryPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:     SessionQueryFactoryName,
		Provides: []plugin.ServiceRef{sessionquery.ServiceKey.Ref()},
		Requires: []plugin.ServiceRef{session.StoreService.Ref(), sesspersist.Service.Ref()},
	}
}

func (instance *sessionQueryPlugin) Apply(
	requestContext context.Context,
	pluginScope *plugin.Scope,
) error {
	store, sessionsFound := plugin.Require(pluginScope, session.StoreService)
	persistence, persistenceFound := plugin.Require(pluginScope, sesspersist.Service)
	if !sessionsFound || !persistenceFound {
		return errors.New("assembly: Session Query dependencies are unavailable")
	}
	derivedIndex, err := querysqlite.Open(requestContext, querysqlite.Config{
		Path: instance.settings.Path, JournalMode: instance.settings.JournalMode,
		SnippetCodePoints: instance.settings.SnippetCodePoints,
	})
	if err != nil {
		return err
	}
	queries, err := sessionquery.New(pluginScope, store, persistence, derivedIndex, sessionquery.Config{
		DefaultLimit: instance.settings.DefaultLimit, MaximumLimit: instance.settings.MaximumLimit,
		ReadWindowMax:               instance.settings.ReadWindowMax,
		PersistedInspectConcurrency: instance.settings.PersistedInspectConcurrency,
	})
	if err != nil {
		_ = derivedIndex.Close(requestContext)
		return err
	}
	_, err = plugin.Provide(pluginScope, sessionquery.ServiceKey, sessionquery.QueryService(queries))
	return err
}
