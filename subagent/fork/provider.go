// Package fork provides in-process Subagent children seeded from a parent.
package fork

import (
	"context"
	"errors"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childscope"
	"github.com/gorenx/goren/subagent/internal/inprocess"
)

const (
	// PluginName is the canonical fork Provider Plugin name.
	PluginName = "@deepseek-ai/dsh-subagent-fork-in-process"
	// DefaultProviderName is the default Provider registry identity.
	DefaultProviderName = "fork"
)

// Provider creates children from the parent's last balanced turn prefix.
type Provider struct {
	plugin.Base
	name         string
	driver       *inprocess.Driver
	registration subagent.ProviderRegistration
}

// New constructs an inactive fork Provider Plugin.
func New(providerName string) (*Provider, error) {
	if err := validateProviderName(providerName); err != nil {
		return nil, err
	}
	return &Provider{
		name: providerName,
	}, nil
}

// Manifest declares Provider registration and in-process driver dependencies.
func (*Provider) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[subagent.ProviderRegistry](),
			plugin.ServiceOf[agent.Registry](),
		},
		Optional: []plugin.ServiceType{
			plugin.ServiceOf[approval.DelegationPolicy](),
		},
	}
}

// Apply constructs the shared driver and registers this exact Provider.
func (owner *Provider) Apply(requestContext context.Context) error {
	providers, requireErr := plugin.Require[subagent.ProviderRegistry](owner)
	if requireErr != nil {
		return requireErr
	}
	agents, requireErr := plugin.Require[agent.Registry](owner)
	if requireErr != nil {
		return requireErr
	}
	delegationPolicy, _ := plugin.Resolve[approval.DelegationPolicy](owner)
	driver, driverErr := inprocess.New(
		agents,
		childscope.NewOneShot(delegationPolicy),
	)
	if driverErr != nil {
		return driverErr
	}
	registration, registerErr := providers.RegisterProvider(
		requestContext,
		owner,
	)
	if registerErr != nil {
		return registerErr
	}
	owner.driver = driver
	owner.registration = registration
	return nil
}

// Dispose prevents new registry resolution without disturbing accepted Runs.
func (owner *Provider) Dispose(closeContext context.Context) error {
	if owner.registration == nil {
		return nil
	}
	unregisterErr := owner.registration.Unregister(closeContext)
	owner.registration = nil
	return unregisterErr
}

// Name returns the exact Provider registry identity.
func (owner *Provider) Name() string {
	return owner.name
}

// Capabilities reports every supported one-shot input.
func (*Provider) Capabilities() subagent.Capabilities {
	return subagent.Capabilities{
		OutputSchema: true,
		DepthLimit:   true,
		ToolFilter:   true,
		Persona:      true,
	}
}

// InheritsParentContext reports that fork seeds completed parent history.
func (*Provider) InheritsParentContext() bool {
	return true
}

// Start delegates one terminal child with a captured balanced parent prefix.
func (owner *Provider) Start(
	requestContext context.Context,
	request subagent.ResolvedStartRequest,
) (subagent.Run, error) {
	if owner.driver == nil {
		return nil, errors.New("subagent: fork Provider is inactive")
	}
	return owner.driver.Start(
		requestContext,
		request,
		inprocess.Options{
			Seed: completedTurnPrefix(request.Parent),
		},
	)
}

// PrepareContinuable captures the prefix once for durable child creation.
func (*Provider) PrepareContinuable(
	requestContext context.Context,
	request subagent.ContinuableCreateRequest,
) (subagent.ContinuableCreateSpec, error) {
	if requestContext == nil {
		return subagent.ContinuableCreateSpec{}, errors.New(
			"subagent: fork preparation context is nil",
		)
	}
	if requestErr := requestContext.Err(); requestErr != nil {
		return subagent.ContinuableCreateSpec{}, requestErr
	}
	if request.Parent == nil {
		return subagent.ContinuableCreateSpec{}, errors.New(
			"subagent: fork preparation requires a parent Agent",
		)
	}
	return subagent.ContinuableCreateSpec{
		Seed: completedTurnPrefix(request.Parent),
	}, nil
}

func completedTurnPrefix(parentAgent agent.Agent) []session.Event {
	if parentAgent == nil || parentAgent.SessionValue() == nil {
		return nil
	}
	events := parentAgent.SessionValue().Events()
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type == session.TurnEndEventName {
			return events[:index+1]
		}
	}
	return nil
}

func validateProviderName(providerName string) error {
	if strings.TrimSpace(providerName) == "" ||
		providerName != strings.TrimSpace(providerName) {
		return errors.New(
			"subagent: fork providerName must be non-empty and trimmed",
		)
	}
	return nil
}

var _ subagent.ContinuableProvider = (*Provider)(nil)
var _ plugin.Plugin = (*Provider)(nil)
