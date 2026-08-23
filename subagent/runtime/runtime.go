// Package runtime composes the repository-private Subagent use-case modules
// into the single Harness-compatible Runtime Plugin.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/llm"
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

// Runtime is the sole Subagent Plugin and capability composition owner.
type Runtime struct {
	plugin.Base

	mutex         sync.RWMutex
	providers     *providerregistry.Registry
	oneShots      *oneshot.Service
	continuations *continuation.Manager
	setups        *setupregistry.Registry
}

// New constructs an inactive Runtime.
func New() *Runtime {
	owner := &Runtime{}
	owner.providers = providerregistry.New(owner)
	owner.oneShots = oneshot.New(owner.providers, owner)
	owner.setups = setupregistry.New()
	return owner
}

// Manifest provides the Subagent domain's independent consumer capabilities.
func (*Runtime) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: subagent.PluginName,
		Provides: []plugin.ServiceType{
			plugin.ServiceOf[subagent.ProviderRegistry](),
			plugin.ServiceOf[subagent.OneShotService](),
			plugin.ServiceOf[subagent.ContinuableService](),
			plugin.ServiceOf[subagent.SetupRegistry](),
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

// Apply resolves the optional continuable dependencies and enables that use
// case when Agent and Session ownership are composed.
func (owner *Runtime) Apply(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("subagent: Runtime Apply context is nil")
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
	continuationManager, managerErr := continuation.New(
		continuation.Dependencies{
			Agents:      agentRegistry,
			Sessions:    liveSessions,
			Persistence: sessionPersistence,
			Providers:   owner.providers,
			Lifecycle:   owner,
			Composer: composition.New(
				approvalService,
				owner.setups,
			),
		},
	)
	if managerErr != nil {
		return managerErr
	}
	owner.mutex.Lock()
	owner.continuations = continuationManager
	owner.mutex.Unlock()
	return nil
}

// Dispose drains continuable Activations before clearing Provider state.
func (owner *Runtime) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	owner.mutex.RLock()
	continuationManager := owner.continuations
	owner.mutex.RUnlock()
	var drainErr error
	if continuationManager != nil {
		drainErr = continuationManager.Drain(
			context.WithoutCancel(closeContext),
		)
	}
	owner.mutex.Lock()
	owner.continuations = nil
	owner.mutex.Unlock()
	danglingSetups, setupErr := owner.setups.Clear(
		context.WithoutCancel(closeContext),
	)
	if danglingSetups != 0 {
		setupErr = errors.Join(
			setupErr,
			fmt.Errorf(
				"subagent: Runtime stopped with %d registered Setup(s)",
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
			"subagent: Runtime stopped with %d registered Provider(s)",
			danglingProviders,
		),
	)
}

// RegisterProvider publishes one exact named Provider.
func (owner *Runtime) RegisterProvider(
	requestContext context.Context,
	candidate subagent.Provider,
) (subagent.ProviderRegistration, error) {
	return owner.providers.Register(requestContext, candidate)
}

// GetProvider returns the exact current Provider registered under name.
func (owner *Runtime) GetProvider(name string) (subagent.Provider, bool) {
	return owner.providers.Get(name)
}

// ListProviders returns Provider names in successful registration order.
func (owner *Runtime) ListProviders() []string {
	return owner.providers.List()
}

// RegisterContinuableSetup publishes one ordered child contribution.
func (owner *Runtime) RegisterContinuableSetup(
	contribution subagent.Setup,
) (subagent.SetupRegistration, error) {
	return owner.setups.Register(contribution)
}

// Start delegates one-shot admission to the one-shot module.
func (owner *Runtime) Start(
	requestContext context.Context,
	selectedName string,
	startInput subagent.StartRequest,
) (subagent.Run, error) {
	return owner.oneShots.Start(requestContext, selectedName, startInput)
}

// StartContinuable creates one durable continuable child.
func (owner *Runtime) StartContinuable(
	requestContext context.Context,
	startSpec subagent.ContinuableStartSpec,
) (subagent.ContinuableStart, error) {
	continuationManager, requireErr := owner.requireContinuations()
	if requireErr != nil {
		return subagent.ContinuableStart{}, requireErr
	}
	return continuationManager.Start(requestContext, startSpec)
}

// Followup delivers one later FIFO turn to a resident or resumed child.
func (owner *Runtime) Followup(
	requestContext context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	content []llm.ContentBlock,
	options subagent.FollowupOptions,
) (llm.MessageID, error) {
	continuationManager, requireErr := owner.requireContinuations()
	if requireErr != nil {
		return "", requireErr
	}
	return continuationManager.Followup(
		requestContext,
		parentAgent,
		childID,
		content,
		options,
	)
}

// Interrupt signals one authorized resident child without waiting for idle.
func (owner *Runtime) Interrupt(
	targetID session.SessionID,
	authority subagent.InterruptAuthority,
) error {
	owner.mutex.RLock()
	continuationManager := owner.continuations
	owner.mutex.RUnlock()
	if continuationManager == nil {
		return nil
	}
	return continuationManager.Interrupt(targetID, authority)
}

// ReportFrom delivers selected child content to its live direct parent.
func (owner *Runtime) ReportFrom(
	requestContext context.Context,
	childAgent agent.Agent,
	content []llm.ContentBlock,
	options subagent.ReportOptions,
) (llm.MessageID, error) {
	continuationManager, requireErr := owner.requireContinuations()
	if requireErr != nil {
		return "", requireErr
	}
	return continuationManager.ReportFrom(
		requestContext,
		childAgent,
		content,
		options,
	)
}

// DrainContinuableChildren releases selected resident direct-child forests.
func (owner *Runtime) DrainContinuableChildren(
	requestContext context.Context,
	parentAgent agent.Agent,
	childIDs []session.SessionID,
) error {
	continuationManager, requireErr := owner.requireContinuations()
	if requireErr != nil {
		return requireErr
	}
	return continuationManager.DrainChildren(
		requestContext,
		parentAgent,
		childIDs,
	)
}

// DrainContinuableDescendants releases descendant forests below exact roots.
func (owner *Runtime) DrainContinuableDescendants(
	requestContext context.Context,
	parents []agent.Agent,
) error {
	continuationManager, requireErr := owner.requireContinuations()
	if requireErr != nil {
		return requireErr
	}
	return continuationManager.DrainDescendants(requestContext, parents)
}

// ObserveEvent routes Agent Inbox and disposal facts to continuation.
func (owner *Runtime) ObserveEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	owner.mutex.RLock()
	continuationManager := owner.continuations
	owner.mutex.RUnlock()
	if continuationManager == nil {
		return nil
	}
	switch notice := fact.(type) {
	case agent.InboxClaimed:
		continuationManager.MessageLeftInbox(
			notice.Subject,
			notice.Message.StableID(),
		)
	case agent.InboxDiscarded:
		continuationManager.MessageLeftInbox(
			notice.Subject,
			notice.Message.StableID(),
		)
	case agent.Disposed:
		continuationManager.AgentDisposed(notice.Subject)
	}
	return nil
}

func (owner *Runtime) requireContinuations() (*continuation.Manager, error) {
	owner.mutex.RLock()
	continuationManager := owner.continuations
	owner.mutex.RUnlock()
	if continuationManager == nil {
		return nil, &subagent.Error{
			Code: subagent.ErrorContinuationUnavailable,
			Message: "continuable subagents require the Agent Registry and " +
				"Session LiveStore",
		}
	}
	return continuationManager, nil
}

// Added publishes a vetoable Provider registration fact.
func (owner *Runtime) Added(
	requestContext context.Context,
	candidate subagent.Provider,
) error {
	return plugin.Publish(
		requestContext,
		owner,
		subagent.ProviderAdded{
			Provider: candidate,
		},
	)
}

// Removed publishes best-effort Provider cleanup.
func (owner *Runtime) Removed(requestContext context.Context, name string) {
	_ = plugin.Publish(
		requestContext,
		owner,
		subagent.ProviderRemoved{
			Name: name,
		},
	)
}

// Started publishes an accepted one-shot lifecycle fact.
func (*Runtime) Started(parentAgent agent.Agent, fact subagent.Started) {
	_ = plugin.Publish(context.Background(), parentAgent, fact)
}

// Ended publishes a terminal one-shot lifecycle fact.
func (*Runtime) Ended(parentAgent agent.Agent, fact subagent.Ended) {
	_ = plugin.Publish(context.Background(), parentAgent, fact)
}

var _ subagent.ProviderRegistry = (*Runtime)(nil)
var _ subagent.OneShotService = (*Runtime)(nil)
var _ subagent.ContinuableService = (*Runtime)(nil)
var _ subagent.SetupRegistry = (*Runtime)(nil)
var _ plugin.EventObserver = (*Runtime)(nil)
