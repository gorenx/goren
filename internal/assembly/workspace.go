package assembly

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sessionpersistence "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/workspace"
)

// WorkspaceConfig is empty because persistence is supplied by an adapter service.
type WorkspaceConfig struct{}

type workspaceFactory struct{}

func (workspaceFactory) Name() string { return WorkspaceFactoryName }

func (workspaceFactory) DecodeConfig(rawConfig json.RawMessage) (WorkspaceConfig, error) {
	return plugin.DecodeStrictConfig[WorkspaceConfig](rawConfig, nil)
}

func (workspaceFactory) New(context.Context, WorkspaceConfig) (plugin.Plugin, error) {
	return &workspacePlugin{}, nil
}

type workspacePlugin struct{}

func (*workspacePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:     WorkspaceFactoryName,
		Provides: []plugin.ServiceRef{workspace.Service.Ref()},
		Requires: []plugin.ServiceRef{
			workspace.BackendService.Ref(), session.StoreService.Ref(), sessionpersistence.Service.Ref(),
		},
	}
}

func (*workspacePlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	storage, storageFound := plugin.Require(pluginScope, workspace.BackendService)
	sessionStore, sessionsFound := plugin.Require(pluginScope, session.StoreService)
	durability, persistenceFound := plugin.Require(pluginScope, sessionpersistence.Service)
	if !storageFound || !sessionsFound || !persistenceFound {
		return errors.New("assembly: Workspace dependencies are unavailable")
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
	persistence sessionpersistence.Persistence
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
		var missing *sessionpersistence.NotFoundError
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
