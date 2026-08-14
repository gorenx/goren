package assembly

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// SessionConfig is intentionally empty: storage belongs to later adapter
// plugins and cannot be selected inside the in-memory Session provider.
type SessionConfig struct{}

type sessionFactory struct{}

func (sessionFactory) Name() string {
	return SessionFactoryName
}

func (sessionFactory) DecodeConfig(rawConfig json.RawMessage) (SessionConfig, error) {
	return plugin.DecodeStrictConfig[SessionConfig](rawConfig, nil)
}

func (sessionFactory) New(context.Context, SessionConfig) (plugin.Plugin, error) {
	return &sessionPlugin{}, nil
}

type sessionPlugin struct{}

func (*sessionPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: SessionFactoryName, Provides: []plugin.ServiceRef{session.StoreService.Ref()}}
}

func (*sessionPlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	registry, err := session.NewMemoryStore(pluginScope, session.MemoryStoreOptions{})
	if err != nil {
		return err
	}
	if err := pluginScope.Effect(requestContext, "sessions", func(context.Context) (plugin.Disposer, error) {
		return registry.Close, nil
	}); err != nil {
		return err
	}
	_, err = plugin.Provide(pluginScope, session.StoreService, session.Store(registry))
	return err
}
