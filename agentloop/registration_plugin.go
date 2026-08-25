package agentloop

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
)

// registrationPlugin registers the Agent Loop Factory after ordinary Plugins are
// active and closes construction admission before per-Agent Scopes stop.
type registrationPlugin struct {
	plugin.Base
	mutex        sync.Mutex
	loop         *Plugin
	registration agent.FactoryRegistration
}

func (*registrationPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName + "/factory-registration",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[agent.FactoryRegistrar](),
		},
	}
}

func (owner *registrationPlugin) Apply(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("agentloop: Factory registration Plugin Apply Context is nil")
	}
	if err := requestContext.Err(); err != nil {
		return err
	}
	registrar, err := plugin.Require[agent.FactoryRegistrar](owner)
	if err != nil {
		return err
	}
	owner.loop.mutex.RLock()
	loopFactory := owner.loop.factory
	owner.loop.mutex.RUnlock()
	if loopFactory == nil {
		return errors.New("agentloop: Factory is unavailable")
	}
	registration, err := registrar.RegisterFactory(loopFactory)
	if err != nil {
		return err
	}
	owner.mutex.Lock()
	owner.registration = registration
	owner.mutex.Unlock()
	return requestContext.Err()
}

func (owner *registrationPlugin) Dispose(context.Context) error {
	owner.mutex.Lock()
	registration := owner.registration
	owner.registration = nil
	owner.mutex.Unlock()
	if registration != nil {
		registration.Close()
	}
	return nil
}

var _ plugin.Plugin = (*registrationPlugin)(nil)
