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
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/catalog"
	"github.com/gorenx/goren/subagent/internal/childscope"
	"github.com/gorenx/goren/subagent/internal/continuation"
	activationextension "github.com/gorenx/goren/subagent/internal/extension"
	"github.com/gorenx/goren/subagent/internal/oneshot"
	providerregistry "github.com/gorenx/goren/subagent/internal/provider"
)

// Plugin owns Subagent module assembly and activation lifecycle. The published
// business capabilities are independent objects.
type Plugin struct {
	plugin.Base

	providers     *providerregistry.Registry
	oneShots      *oneshot.Service
	continuations *continuation.Service
	extensions    *activationextension.Registry
	catalog       *catalog.Service
	events        *eventPublisher
	projections   []sessionprojection.UnitHandle
	failures      *failureReporter
}

// New constructs an inactive Subagent Plugin and its stable business Services.
func New(options RuntimeOptions) *Plugin {
	reporter := options.ObserverError
	if reporter == nil {
		reporter = func(error) {}
	}
	owner := &Plugin{}
	owner.failures = &failureReporter{
		report: reporter,
	}
	owner.events = &eventPublisher{
		owner: owner,
	}
	owner.providers = providerregistry.New(owner.events)
	owner.oneShots = oneshot.New(owner.providers, owner.events)
	owner.continuations = continuation.NewService()
	owner.extensions = activationextension.New()
	owner.catalog = catalog.New()
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
			plugin.NewProvidedService[subagent.ExtensionRegistry](owner.extensions),
			plugin.NewProvidedService[subagent.Catalog](owner.catalog),
		},
		Optional: []plugin.ServiceType{
			plugin.ServiceOf[agent.Registry](),
			plugin.ServiceOf[agent.Constructor](),
			plugin.ServiceOf[agent.RuntimeDescendants](),
			plugin.ServiceOf[session.LiveStore](),
			plugin.ServiceOf[persistence.Persistence](),
			plugin.ServiceOf[approval.DelegationPolicy](),
			plugin.ServiceOf[sessionprojection.Registry](),
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
	agentConstructor, _ := plugin.Resolve[agent.Constructor](owner)
	runtimeDescendants, _ := plugin.Resolve[agent.RuntimeDescendants](owner)
	liveSessions, _ := plugin.Resolve[session.LiveStore](owner)
	sessionPersistence, _ := plugin.Resolve[persistence.Persistence](owner)
	approvalService, _ := plugin.Resolve[approval.DelegationPolicy](owner)
	projectionRegistry, _ := plugin.Resolve[sessionprojection.Registry](owner)
	if err := owner.registerProjections(projectionRegistry); err != nil {
		return err
	}
	if err := owner.catalog.Enable(
		liveSessions,
		sessionPersistence,
		projectionRegistry,
	); err != nil {
		return err
	}
	if agentRegistry == nil || agentConstructor == nil ||
		runtimeDescendants == nil || liveSessions == nil {
		return nil
	}
	manager, err := continuation.New(
		continuation.Dependencies{
			Agents:      agentRegistry,
			Constructor: agentConstructor,
			Descendants: runtimeDescendants,
			Sessions:    liveSessions,
			Persistence: sessionPersistence,
			Providers:   owner.providers,
			Lifecycle:   owner.events,
			Failures:    owner.failures,
			Scopes: childscope.NewContinuable(
				approvalService,
				owner.extensions,
			),
		},
	)
	if err != nil {
		return err
	}
	return owner.continuations.Enable(manager)
}

// Dispose requests managed close for continuable Activations before clearing
// Extension and Provider registrations owned by this activation.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	rollbackContext := context.WithoutCancel(closeContext)
	continuationErr := owner.continuations.Disable(rollbackContext)
	owner.catalog.Disable()
	danglingExtensions, extensionErr := owner.extensions.Clear(rollbackContext)
	if danglingExtensions != 0 {
		extensionErr = errors.Join(
			extensionErr,
			fmt.Errorf(
				"subagent: Plugin stopped with %d registered Extension(s)",
				danglingExtensions,
			),
		)
	}
	danglingProviders := owner.providers.Clear()
	projectionErr := owner.releaseProjections(rollbackContext)
	if danglingProviders == 0 {
		return errors.Join(continuationErr, extensionErr, projectionErr)
	}
	return errors.Join(
		continuationErr,
		extensionErr,
		projectionErr,
		fmt.Errorf(
			"subagent: Plugin stopped with %d registered Provider(s)",
			danglingProviders,
		),
	)
}

var _ plugin.Plugin = (*Plugin)(nil)
