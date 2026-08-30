package agent

import (
	"context"
	"errors"

	"github.com/gorenx/goren/plugin"
)

// RegistryPlugin binds one Factory to RegistryService and publishes the
// independent consumer-facing lifecycle capabilities.
type RegistryPlugin struct {
	plugin.Base
	service *RegistryService
}

// NewRegistryPlugin adapts one Registry business Service to Plugin Runtime.
func NewRegistryPlugin(service *RegistryService) (*RegistryPlugin, error) {
	if service == nil {
		return nil, errors.New("agent: Registry Service is required")
	}
	return &RegistryPlugin{
		service: service,
	}, nil
}

// Manifest publishes narrow capability views of the same lifecycle Service.
func (owner *RegistryPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[Factory](),
		},
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[Registry](owner.service),
			plugin.NewProvidedService[Constructor](owner.service),
			plugin.NewProvidedService[ScopeSetup](owner.service),
			plugin.NewProvidedService[RuntimeDescendants](owner.service),
			plugin.NewProvidedService[EventDispatcher](owner.service),
		},
	}
}

// Apply validates startup cancellation before Service publication.
func (owner *RegistryPlugin) Apply(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("agent: Registry Plugin Apply Context is nil")
	}
	if err := requestContext.Err(); err != nil {
		return err
	}
	agentFactory, err := plugin.Require[Factory](owner)
	if err != nil {
		return err
	}
	return owner.service.bind(agentFactory)
}

// Dispose deactivates the Registry after dependent Plugins stop and joins every
// exact Agent lifecycle admitted by this module activation.
func (owner *RegistryPlugin) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	return owner.service.deactivate(context.WithoutCancel(closeContext))
}

var _ plugin.Plugin = (*RegistryPlugin)(nil)
