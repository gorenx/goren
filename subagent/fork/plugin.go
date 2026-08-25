package fork

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childscope"
	"github.com/gorenx/goren/subagent/internal/inprocess"
)

// Plugin resolves runtime dependencies, constructs the business Provider, and
// owns its exact registry registration.
type Plugin struct {
	plugin.Base
	name         string
	registration subagent.ProviderRegistration
}

func NewPlugin(providerName string) (*Plugin, error) {
	if err := validateProviderName(providerName); err != nil {
		return nil, err
	}
	return &Plugin{
		name: providerName,
	}, nil
}

func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[subagent.ProviderRegistry](),
			plugin.ServiceOf[agent.Registry](),
			plugin.ServiceOf[agent.Constructor](),
		},
		Optional: []plugin.ServiceType{
			plugin.ServiceOf[approval.DelegationPolicy](),
		},
	}
}

func (owner *Plugin) Apply(requestContext context.Context) error {
	providers, err := plugin.Require[subagent.ProviderRegistry](owner)
	if err != nil {
		return err
	}
	agents, err := plugin.Require[agent.Registry](owner)
	if err != nil {
		return err
	}
	constructor, err := plugin.Require[agent.Constructor](owner)
	if err != nil {
		return err
	}
	delegationPolicy, _ := plugin.Resolve[approval.DelegationPolicy](owner)
	driver, err := inprocess.New(
		agents,
		constructor,
		childscope.NewOneShot(delegationPolicy),
	)
	if err != nil {
		return err
	}
	candidateProvider, err := newProvider(owner.name, driver)
	if err != nil {
		return err
	}
	registration, err := providers.RegisterProvider(
		requestContext,
		candidateProvider,
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
