package agentloop

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop/internal/visiblecontext"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
)

// RuntimeOptions contains process-local failure reporting policy.
type RuntimeOptions struct {
	ObserverError func(error)
}

// Plugin adapts Agent Loop construction and per-Agent Scope effects to the
// Plugin Runtime. Agent construction is owned by Factory; Agent lifecycle and
// runtime parent-child ordering are owned by agent.Registry.
type Plugin struct {
	plugin.Base
	mutex                sync.RWMutex
	maxParallelToolCalls int
	reportObserverError  func(error)
	visibleContexts      *visiblecontext.Directory
	startup              *StartupPlan
	constructor          agent.Constructor
	registration         *registrationPlugin
	scopes               *ScopeSet
}

// New constructs an inactive Agent Loop Plugin from validated runtime
// settings. Raw configuration belongs to agentloop/factory.
func New(runtimeSettings Settings, policies RuntimeOptions) (*Plugin, error) {
	validated, err := validateSettings(runtimeSettings)
	if err != nil {
		return nil, err
	}
	reportObserverError := policies.ObserverError
	if reportObserverError == nil {
		reportObserverError = func(error) {}
	}
	owner := &Plugin{
		maxParallelToolCalls: validated.MaxParallelToolCalls,
		reportObserverError:  reportObserverError,
		visibleContexts:      visiblecontext.NewDirectory(),
		startup: newStartupPlan(
			validated.StartupAgents,
		),
	}
	owner.registration = &registrationPlugin{}
	owner.scopes = newScopeSet()
	return owner, nil
}

func (owner *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[agent.Constructor](),
			plugin.ServiceOf[session.LiveStore](),
		},
		Optional: []plugin.ServiceType{
			plugin.ServiceOf[sesspersist.Persistence](),
		},
		Events: []plugin.EventSubscription{
			plugin.EventOf[session.EventAppended](),
		},
		Children: []plugin.ChildPlugin{
			{
				Instance:  owner.scopes,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
			{
				Instance:  owner.registration,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationCommit,
			},
		},
	}
}

func (owner *Plugin) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	constructor, err := plugin.Require[agent.Constructor](owner)
	if err != nil {
		return err
	}
	sessions, err := plugin.Require[session.LiveStore](owner)
	if err != nil {
		return err
	}
	persistence, _ := plugin.Resolve[sesspersist.Persistence](owner)
	agentFactory := newFactory(
		owner.maxParallelToolCalls,
		sessions,
		persistence,
		owner.reportObserverError,
		owner.visibleContexts,
		owner.scopes,
	)
	owner.mutex.Lock()
	owner.constructor = constructor
	owner.mutex.Unlock()
	if err = owner.registration.bindFactory(agentFactory); err != nil {
		return err
	}
	return requestContext.Err()
}

// Dispose releases Agent Loop-local VisibleContext registrations after the
// commit-phase Factory adapter and every per-Agent Scope have already stopped.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	var closeErr error
	owner.mutex.Lock()
	owner.constructor = nil
	owner.mutex.Unlock()
	if routed := owner.visibleContexts.Clear(); routed != 0 {
		closeErr = errors.Join(
			closeErr,
			fmt.Errorf(
				"agentloop: Plugin stopped with %d live VisibleContext registration(s)",
				routed,
			),
		)
	}
	return closeErr
}

// ObserveEvent routes exact Session commits to the corresponding private
// per-Agent VisibleContext.
func (owner *Plugin) ObserveEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	appended, matches := fact.(session.EventAppended)
	if !matches {
		return nil
	}
	owner.visibleContexts.Observe(appended)
	return nil
}

// StartConfiguredAgents runs the one-shot boot transaction after
// plugin.Runtime.Start through the canonical Registry API.
func (owner *Plugin) StartConfiguredAgents(
	requestContext context.Context,
) ([]agent.Handle, error) {
	owner.mutex.RLock()
	constructor := owner.constructor
	owner.mutex.RUnlock()
	return owner.startup.start(requestContext, constructor)
}

func validateAgentOptions(agentOptions agent.Options) error {
	if agentOptions.MaxTokens != nil &&
		(*agentOptions.MaxTokens <= 0 ||
			int64(*agentOptions.MaxTokens) > maxSafeInteger) {
		return errors.New(
			"agentloop: Agent maxTokens must be a positive safe integer",
		)
	}
	return nil
}

var _ plugin.Plugin = (*Plugin)(nil)
