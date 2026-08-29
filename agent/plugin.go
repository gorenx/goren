package agent

import (
	"context"
	"errors"

	"github.com/gorenx/goren/plugin"
)

// RegistryPlugin publishes the independent consumer-facing capabilities of
// one RegistryService. It owns no Agent epoch or close transaction state.
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
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[Registry](owner.service),
			plugin.NewProvidedService[Constructor](owner.service),
			plugin.NewProvidedService[ScopeProvisioning](owner.service),
			plugin.NewProvidedService[RuntimeDescendants](owner.service),
			plugin.NewProvidedService[FactoryRegistrar](owner.service),
		},
	}
}

// Apply validates startup cancellation before Service publication.
func (*RegistryPlugin) Apply(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("agent: Registry Plugin Apply Context is nil")
	}
	return requestContext.Err()
}

// Dispose closes the Registry after every dependent Plugin and Agent Scope has
// stopped. Shutdown remains idempotent for already retired Agent epochs.
func (owner *RegistryPlugin) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	return owner.service.Shutdown(context.WithoutCancel(closeContext))
}

var _ plugin.Plugin = (*RegistryPlugin)(nil)
