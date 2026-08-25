package agentloop

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
)

// factoryAdapter registers the Agent Loop Factory after ordinary Plugins are
// active and closes construction admission before per-Agent Scopes stop.
type factoryAdapter struct {
	plugin.Base
	mutex        sync.Mutex
	owner        *Plugin
	registration agent.FactoryRegistration
}

func (*factoryAdapter) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName + "/factory",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[agent.FactoryRegistrar](),
		},
	}
}

func (adapter *factoryAdapter) Apply(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("agentloop: Factory adapter Apply Context is nil")
	}
	if err := requestContext.Err(); err != nil {
		return err
	}
	registrar, err := plugin.Require[agent.FactoryRegistrar](adapter)
	if err != nil {
		return err
	}
	adapter.owner.mutex.RLock()
	loopFactory := adapter.owner.factory
	adapter.owner.mutex.RUnlock()
	if loopFactory == nil {
		return errors.New("agentloop: Factory is unavailable")
	}
	registration, err := registrar.RegisterFactory(loopFactory)
	if err != nil {
		return err
	}
	adapter.mutex.Lock()
	adapter.registration = registration
	adapter.mutex.Unlock()
	return requestContext.Err()
}

func (adapter *factoryAdapter) Dispose(context.Context) error {
	adapter.mutex.Lock()
	registration := adapter.registration
	adapter.registration = nil
	adapter.mutex.Unlock()
	if registration != nil {
		registration.Close()
	}
	return nil
}

var _ plugin.Plugin = (*factoryAdapter)(nil)
