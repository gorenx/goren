package agentloop

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
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
	failures             observerFailureReporter
	runtimeContextEvents *runtimeContextRouter
	startup              *configuredAgentStarter
	constructor          agent.Constructor
	factory              *Factory
	adapter              *factoryAdapter
}

// New constructs an inactive Agent Loop Plugin from validated runtime
// settings. Raw configuration belongs to agentloop/factory.
func New(runtimeSettings Settings, policies RuntimeOptions) (*Plugin, error) {
	validated, err := validateSettings(runtimeSettings)
	if err != nil {
		return nil, err
	}
	owner := &Plugin{
		maxParallelToolCalls: validated.MaxParallelToolCalls,
		failures:             newObserverFailureReporter(policies.ObserverError),
		runtimeContextEvents: newRuntimeContextRouter(),
		startup: newConfiguredAgentStarter(
			validated.StartupAgents,
		),
	}
	owner.adapter = &factoryAdapter{
		owner: owner,
	}
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
				Instance:  owner.adapter,
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
	loopFactory := newFactory(
		owner.maxParallelToolCalls,
		sessions,
		persistence,
		owner.failures,
		owner.runtimeContextEvents,
		pluginScopeHost{
			owner: owner,
		},
	)
	owner.mutex.Lock()
	owner.constructor = constructor
	owner.factory = loopFactory
	owner.mutex.Unlock()
	return requestContext.Err()
}

// Dispose releases Agent Loop-local projections after the commit-phase Factory
// adapter and every per-Agent Scope have already stopped.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	var closeErr error
	owner.mutex.Lock()
	owner.constructor = nil
	owner.factory = nil
	owner.mutex.Unlock()
	if routed := owner.runtimeContextEvents.clear(); routed != 0 {
		closeErr = errors.Join(
			closeErr,
			fmt.Errorf(
				"agentloop: Plugin stopped with %d live runtime-context projection(s)",
				routed,
			),
		)
	}
	return closeErr
}

// ObserveEvent routes exact Session commits to the corresponding private
// per-Agent runtime-context projection.
func (owner *Plugin) ObserveEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	appended, matches := fact.(session.EventAppended)
	if !matches {
		return nil
	}
	owner.runtimeContextEvents.accept(appended)
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

func validateAgentOptions(loopOptions agent.Options) error {
	if loopOptions.MaxTokens != nil &&
		(*loopOptions.MaxTokens <= 0 ||
			int64(*loopOptions.MaxTokens) > maxSafeInteger) {
		return errors.New(
			"agentloop: Agent maxTokens must be a positive safe integer",
		)
	}
	return nil
}

var _ plugin.Plugin = (*Plugin)(nil)
