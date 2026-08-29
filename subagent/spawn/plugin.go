package spawn

import (
	"context"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent"
)

// Plugin constructs the spawn SeedBuilder and owns its exact registration.
type Plugin struct {
	plugin.Base
	name         string
	registration subagent.SeedBuilderRegistration
}

func NewPlugin(builderName string) (*Plugin, error) {
	if err := validateBuilderName(builderName); err != nil {
		return nil, err
	}
	return &Plugin{
		name: builderName,
	}, nil
}

func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[subagent.SeedBuilderRegistry](),
		},
	}
}

func (owner *Plugin) Apply(requestContext context.Context) error {
	builders, err := plugin.Require[subagent.SeedBuilderRegistry](owner)
	if err != nil {
		return err
	}
	candidateBuilder, err := newBuilder(owner.name)
	if err != nil {
		return err
	}
	registration, err := builders.Register(
		requestContext,
		candidateBuilder,
	)
	if err != nil {
		return err
	}
	owner.registration = registration
	return nil
}

func (owner *Plugin) Dispose(closeContext context.Context) error {
	if owner.registration == nil {
		return nil
	}
	err := owner.registration.Unregister(closeContext)
	owner.registration = nil
	return err
}

var _ plugin.Plugin = (*Plugin)(nil)
