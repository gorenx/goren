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

// Plugin is the global Agent Loop factory and lifecycle owner. It assembles a
// complete private Plugin tree for each Agent, but it does not expose a second
// Loop Service; callers create and resume Agents through agent.Registry.
type Plugin struct {
	plugin.Base
	mutex                sync.RWMutex
	maxParallelToolCalls int
	factoryRegistration  agent.FactoryRegistration
	sessions             session.LiveStore
	persistence          sesspersist.Persistence
	failures             observerFailureReporter
	admission            *constructionGate
	runtimeContextEvents *runtimeContextRouter
	startup              *configuredAgentStarter
}

// New constructs an inactive Agent Loop Plugin from validated runtime
// settings. Raw configuration belongs to agentloop/factory.
func New(runtimeSettings Settings, policies RuntimeOptions) (*Plugin, error) {
	validated, err := validateSettings(runtimeSettings)
	if err != nil {
		return nil, err
	}
	return &Plugin{
		maxParallelToolCalls: validated.MaxParallelToolCalls,
		failures:             newObserverFailureReporter(policies.ObserverError),
		admission:            newConstructionGate(),
		runtimeContextEvents: newRuntimeContextRouter(),
		startup: newConfiguredAgentStarter(
			validated.StartupAgents,
		),
	}, nil
}

// Manifest declares only capabilities needed by global construction and the
// root Session Event subscription. Per-Agent execution dependencies are
// declared by ReactLoopAgent inside each complete dynamic tree.
func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[agent.Registry](),
			plugin.ServiceOf[session.LiveStore](),
		},
		Optional: []plugin.ServiceType{
			plugin.ServiceOf[sesspersist.Persistence](),
		},
		Events: []plugin.EventSubscription{
			plugin.EventOf[session.SessionEventAppended](),
		},
	}
}

// Apply opens construction admission and attaches this exact Factory to the
// Agent Registry. Configured Agents start only after Runtime.Start returns.
func (owner *Plugin) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	agents, err := plugin.Require[agent.Registry](owner)
	if err != nil {
		return err
	}
	sessions, err := plugin.Require[session.LiveStore](owner)
	if err != nil {
		return err
	}
	persistence, _ := plugin.Resolve[sesspersist.Persistence](owner)
	if err = owner.admission.open(); err != nil {
		return err
	}
	owner.mutex.Lock()
	owner.sessions = sessions
	owner.persistence = persistence
	owner.mutex.Unlock()
	registration, err := agents.RegisterFactory(owner)
	if err != nil {
		return err
	}
	owner.mutex.Lock()
	owner.factoryRegistration = registration
	owner.mutex.Unlock()
	return requestContext.Err()
}

// Dispose closes construction after Runtime has stopped every Agent child.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	drainContext := context.WithoutCancel(closeContext)
	drainErr := owner.admission.closeAndWait(drainContext)
	owner.mutex.Lock()
	registration := owner.factoryRegistration
	owner.factoryRegistration = nil
	owner.sessions = nil
	owner.persistence = nil
	owner.mutex.Unlock()
	if registration != nil {
		registration.Unregister()
	}
	if routed := owner.runtimeContextEvents.clear(); routed != 0 {
		drainErr = errors.Join(
			drainErr,
			fmt.Errorf(
				"agentloop: Plugin stopped with %d live runtime-context projection(s)",
				routed,
			),
		)
	}
	return drainErr
}

// ObserveEvent routes exact Session commits to the corresponding private
// per-Agent runtime-context projection.
func (owner *Plugin) ObserveEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	appended, matches := fact.(session.SessionEventAppended)
	if !matches {
		return nil
	}
	owner.runtimeContextEvents.accept(appended)
	return nil
}

// CreateAgent prepares, composes, commits, and publishes one fresh Agent.
func (owner *Plugin) CreateAgent(
	requestContext context.Context,
	options agent.CreateOptions,
) (agent.Handle, error) {
	operationContext, finishConstruction, err :=
		owner.admission.begin(requestContext)
	if err != nil {
		return agent.Handle{}, err
	}
	defer finishConstruction()

	sessions, _, err := owner.constructionServices()
	if err != nil {
		return agent.Handle{}, err
	}
	if options.SessionID == "" {
		return agent.Handle{}, errors.New("agentloop: Agent Session id is empty")
	}
	if err = validateAgentOptions(options.AgentOptions); err != nil {
		return agent.Handle{}, err
	}
	identifier := options.SessionID
	conversation, err := sessions.Prepare(
		&identifier,
		session.CreateOptions{
			Seed:     options.Seed,
			Metadata: options.Metadata,
		},
	)
	if err != nil {
		return agent.Handle{}, err
	}
	prepared, err := newPreparedAgent(
		operationContext,
		owner,
		conversation,
		options.AgentOptions,
	)
	if err != nil {
		return agent.Handle{}, err
	}
	return prepared.publish(
		operationContext,
		options.Setup,
		agent.SessionStartup,
	)
}

// ResumeAgent restores an unpublished durable Session and mounts one complete
// Agent tree before publication.
func (owner *Plugin) ResumeAgent(
	requestContext context.Context,
	options agent.ResumeOptions,
) (agent.Handle, error) {
	operationContext, finishConstruction, err :=
		owner.admission.begin(requestContext)
	if err != nil {
		return agent.Handle{}, err
	}
	defer finishConstruction()

	_, persistence, err := owner.constructionServices()
	if err != nil {
		return agent.Handle{}, err
	}
	if options.SessionID == "" {
		return agent.Handle{}, errors.New("agentloop: resume Session id is empty")
	}
	if err = validateAgentOptions(options.AgentOptions); err != nil {
		return agent.Handle{}, err
	}
	if persistence == nil {
		return agent.Handle{}, errors.New(
			"agentloop: session persistence is not configured",
		)
	}
	preparation, err := persistence.Prepare(
		operationContext,
		options.SessionID,
	)
	if err != nil {
		return agent.Handle{}, err
	}
	defer preparation.Dispose()
	prepared, err := newPreparedAgent(
		operationContext,
		owner,
		preparation.UnpublishedSession(),
		options.AgentOptions,
	)
	if err != nil {
		return agent.Handle{}, err
	}
	return prepared.publish(
		operationContext,
		options.Setup,
		agent.SessionResume,
	)
}

// StartConfiguredAgents runs the one-shot boot transaction after
// plugin.Runtime.Start. Failure is terminal for this Plugin instance; the
// composition root must stop Runtime.
func (owner *Plugin) StartConfiguredAgents(
	requestContext context.Context,
) ([]agent.Handle, error) {
	return owner.startup.start(requestContext, owner)
}

func (owner *Plugin) constructionServices() (
	session.LiveStore,
	sesspersist.Persistence,
	error,
) {
	owner.mutex.RLock()
	defer owner.mutex.RUnlock()
	if owner.sessions == nil {
		return nil, nil, errors.New("agentloop: Agent Loop is not active")
	}
	return owner.sessions, owner.persistence, nil
}

func validateAgentOptions(loopOptions agent.Options) error {
	if loopOptions.MaxTokens != nil &&
		(*loopOptions.MaxTokens <= 0 ||
			int64(*loopOptions.MaxTokens) > maxSafeInteger) {
		return errors.New(
			"agentloop: Agent maxTokens must be a positive safe integer",
		)
	}
	if loopOptions.SubagentDepth != nil &&
		(*loopOptions.SubagentDepth < 0 ||
			*loopOptions.SubagentDepth > maxSafeInteger) {
		return errors.New(
			"agentloop: Agent subagentDepth must be a non-negative safe integer",
		)
	}
	return nil
}

var _ agent.Factory = (*Plugin)(nil)
