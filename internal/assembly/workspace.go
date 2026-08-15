package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/workspace"
	workspaceSqlite "github.com/gorenx/goren/workspace/persistence/sqlite"
)

// WorkspaceConfig configures the Workspace capability. SQLite is its built-in
// Backend adapter and remains invisible to the runtime service graph.
type WorkspaceConfig struct {
	Path        string                      `json:"path"`
	JournalMode workspaceSqlite.JournalMode `json:"journalMode,omitempty"`
}

type workspaceFactory struct{}

func (workspaceFactory) Name() string { return WorkspaceFactoryName }

func (workspaceFactory) DecodeConfig(rawConfig json.RawMessage) (WorkspaceConfig, error) {
	settings, err := plugin.DecodeStrictConfig(rawConfig, func(candidate WorkspaceConfig) error {
		if strings.TrimSpace(candidate.Path) == "" {
			return errors.New("path must be non-empty")
		}
		switch candidate.JournalMode {
		case "", workspaceSqlite.JournalWAL, workspaceSqlite.JournalDelete,
			workspaceSqlite.JournalTruncate, workspaceSqlite.JournalPersist:
		default:
			return errors.New("journalMode must be wal, delete, truncate, or persist")
		}
		return nil
	})
	if err != nil {
		return WorkspaceConfig{}, err
	}
	if settings.JournalMode == "" {
		settings.JournalMode = workspaceSqlite.JournalWAL
	}
	return settings, nil
}

func (workspaceFactory) New(_ context.Context, settings WorkspaceConfig) (plugin.Plugin, error) {
	return &workspacePlugin{settings: settings}, nil
}

type workspacePlugin struct {
	settings WorkspaceConfig
}

func (*workspacePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:     WorkspaceFactoryName,
		Provides: []plugin.ServiceRef{workspace.Service.Ref()},
		Requires: []plugin.ServiceRef{
			session.StoreService.Ref(),
			sesspersist.Service.Ref(),
		},
	}
}

func (instance *workspacePlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	sessionStore, sessionsFound := plugin.Require(pluginScope, session.StoreService)
	durability, persistenceFound := plugin.Require(pluginScope, sesspersist.Service)
	if !sessionsFound || !persistenceFound {
		return errors.New("assembly: Workspace dependencies are unavailable")
	}
	storage, err := workspaceSqlite.Open(
		requestContext,
		workspaceSqlite.Config{
			Path:        instance.settings.Path,
			JournalMode: instance.settings.JournalMode,
		},
	)
	if err != nil {
		return err
	}
	if err := pluginScope.Effect(requestContext, "workspace.persistence.close()",
		func(context.Context) (plugin.Disposer, error) {
			return storage.Close, nil
		}); err != nil {
		_ = storage.Close(requestContext)
		return err
	}
	headers := &workspaceSessionHeaders{sessions: sessionStore, persistence: durability}
	registry, err := workspace.NewRegistry(
		requestContext, pluginScope, storage, headers, workspace.RegistryOptions{},
	)
	if err != nil {
		return err
	}
	_, err = plugin.Provide(pluginScope, workspace.Service, workspace.Registry(registry))
	return err
}

// workspaceSessionHeaders translates Session ownership into the minimal
// immutable-header port consumed by Workspace.
type workspaceSessionHeaders struct {
	sessions    session.Store
	persistence sesspersist.Persistence
}

func (source *workspaceSessionHeaders) Get(
	requestContext context.Context,
	identifier session.SessionID,
) (session.Header, bool, error) {
	if conversation, found := source.sessions.Get(identifier); found {
		return conversation.Header(), true, nil
	}
	loaded, err := source.persistence.Inspect(requestContext, identifier)
	if err != nil {
		var missing *sesspersist.NotFoundError
		if errors.As(err, &missing) {
			return session.Header{}, false, nil
		}
		return session.Header{}, false, err
	}
	return loaded.Header, true, nil
}

func (source *workspaceSessionHeaders) List(requestContext context.Context) ([]session.Header, error) {
	stored, err := source.persistence.List(requestContext)
	if err != nil {
		return nil, err
	}
	byID := make(map[session.SessionID]session.Header, len(stored))
	order := make([]session.SessionID, 0, len(stored))
	for _, header := range stored {
		byID[header.ID] = header
		order = append(order, header.ID)
	}
	for _, conversation := range source.sessions.List() {
		header := conversation.Header()
		if _, found := byID[header.ID]; !found {
			order = append(order, header.ID)
		}
		byID[header.ID] = header
	}
	result := make([]session.Header, 0, len(order))
	for _, identifier := range order {
		result = append(result, byID[identifier])
	}
	return result, nil
}
