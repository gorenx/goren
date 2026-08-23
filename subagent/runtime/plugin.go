// Package runtime composes the repository-private Subagent use-case modules
// into the Harness-compatible Plugin.
package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/composition"
	"github.com/gorenx/goren/subagent/internal/continuation"
	"github.com/gorenx/goren/subagent/internal/oneshot"
	providerregistry "github.com/gorenx/goren/subagent/internal/provider"
	setupregistry "github.com/gorenx/goren/subagent/internal/setup"
)

// Plugin owns Subagent module assembly and activation lifecycle. The published
// business capabilities are independent objects.
type Plugin struct {
	plugin.Base

	providers     *providerregistry.Registry
	oneShots      *oneshot.Service
	continuations *continuation.Service
	setups        *setupregistry.Registry
	events        *eventPublisher
}

// New constructs an inactive Subagent Plugin and its stable business Services.
func New() *Plugin {
	owner := &Plugin{}
	owner.events = &eventPublisher{
		owner: owner,
	}
	owner.providers = providerregistry.New(owner.events)
	owner.oneShots = oneshot.New(owner.providers, owner.events)
	owner.continuations = continuation.NewService()
	owner.setups = setupregistry.New()
	return owner
}

// Manifest publishes the independent Subagent business capabilities.
func (owner *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: subagent.PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[subagent.ProviderRegistry](owner.providers),
			plugin.NewProvidedService[subagent.OneShotService](owner.oneShots),
			plugin.NewProvidedService[subagent.ContinuableService](owner.continuations),
			plugin.NewProvidedService[subagent.SetupRegistry](owner.setups),
		},
		Optional: []plugin.ServiceType{
			plugin.ServiceOf[agent.Registry](),
			plugin.ServiceOf[session.LiveStore](),
			plugin.ServiceOf[persistence.Persistence](),
			plugin.ServiceOf[approval.DelegationPolicy](),
		},
		Events: []plugin.EventSubscription{
			plugin.EventOf[agent.InboxClaimed](),
			plugin.EventOf[agent.InboxDiscarded](),
			plugin.EventOf[agent.Disposed](),
		},
	}
}

// Apply resolves optional continuable dependencies and enables that Service
// when Agent and Session ownership are composed.
func (owner *Plugin) Apply(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("subagent: Plugin Apply context is nil")
	}
	if requestErr := requestContext.Err(); requestErr != nil {
		return requestErr
	}
	agentRegistry, _ := plugin.Resolve[agent.Registry](owner)
	liveSessions, _ := plugin.Resolve[session.LiveStore](owner)
	sessionPersistence, _ := plugin.Resolve[persistence.Persistence](owner)
	approvalService, _ := plugin.Resolve[approval.DelegationPolicy](owner)
	if agentRegistry == nil || liveSessions == nil {
		return nil
	}
	manager, err := continuation.New(
		continuation.Dependencies{
			Agents:      agentRegistry,
			Sessions:    liveSessions,
			Persistence: sessionPersistence,
			Providers:   owner.providers,
			Lifecycle:   owner.events,
			Composer: composition.New(
				approvalService,
				owner.setups,
			),
		},
	)
	if err != nil {
		return err
	}
	return owner.continuations.Enable(manager)
}

// Dispose drains continuable Activations before clearing Setup and Provider
// registrations owned by this activation.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	rollbackContext := context.WithoutCancel(closeContext)
	drainErr := owner.continuations.Disable(rollbackContext)
	danglingSetups, setupErr := owner.setups.Clear(rollbackContext)
	if danglingSetups != 0 {
		setupErr = errors.Join(
			setupErr,
			fmt.Errorf(
				"subagent: Plugin stopped with %d registered Setup(s)",
				danglingSetups,
			),
		)
	}
	danglingProviders := owner.providers.Clear()
	if danglingProviders == 0 {
		return errors.Join(drainErr, setupErr)
	}
	return errors.Join(
		drainErr,
		setupErr,
		fmt.Errorf(
			"subagent: Plugin stopped with %d registered Provider(s)",
			danglingProviders,
		),
	)
}

var _ plugin.Plugin = (*Plugin)(nil)
