package agentloop

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// factoryDependencies are the capabilities required to construct one RLA.
// The AgentLoop Plugin supplies one immutable snapshot while it is active.
type factoryDependencies struct {
	sessions    session.LiveStore
	persistence sesspersist.Persistence
	models      llm.LlmRuntime
	prompts     systemprompt.PromptLayerFactory
	toolLayers  tools.ToolLayerFactory
	events      agentEvents
	waterfalls  agentWaterfalls
}

// Factory constructs independent single-Agent Hosts. It owns no live Agent
// collection and no parent-child relation.
type Factory struct {
	mutex                sync.RWMutex
	active               bool
	constructions        sync.WaitGroup
	dependencies         factoryDependencies
	maxParallelToolCalls int
	reportObserverError  func(error)
}

func newFactory(
	maxParallelToolCalls int,
	reportObserverError func(error),
) *Factory {
	if reportObserverError == nil {
		reportObserverError = func(error) {}
	}
	return &Factory{
		maxParallelToolCalls: maxParallelToolCalls,
		reportObserverError:  reportObserverError,
	}
}

func (owner *Factory) enterRuntime(dependencies factoryDependencies) error {
	if dependencies.sessions == nil || dependencies.models == nil ||
		dependencies.prompts == nil || dependencies.toolLayers == nil ||
		dependencies.events == nil || dependencies.waterfalls == nil {
		return errors.New("agentloop: Factory dependencies are incomplete")
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.active {
		return errors.New("agentloop: Factory is already active")
	}
	owner.dependencies = dependencies
	owner.active = true
	return nil
}

func (owner *Factory) leaveRuntime() {
	owner.mutex.Lock()
	owner.active = false
	owner.dependencies = factoryDependencies{}
	owner.mutex.Unlock()
	owner.constructions.Wait()
}

func (owner *Factory) beginConstruction(
	requestContext context.Context,
) (factoryDependencies, func(), error) {
	if requestContext == nil {
		return factoryDependencies{}, nil, errors.New(
			"agentloop: construction Context is nil",
		)
	}
	if err := requestContext.Err(); err != nil {
		return factoryDependencies{}, nil, err
	}
	owner.mutex.Lock()
	if !owner.active {
		owner.mutex.Unlock()
		return factoryDependencies{}, nil, errors.New(
			"agentloop: Factory is not active",
		)
	}
	dependencies := owner.dependencies
	owner.constructions.Add(1)
	owner.mutex.Unlock()
	var releaseOnce sync.Once
	releaseConstruction := func() {
		releaseOnce.Do(owner.constructions.Done)
	}
	return dependencies, releaseConstruction, nil
}

// CreateAgent constructs one unpublished Host for a fresh Session.
func (owner *Factory) CreateAgent(
	requestContext context.Context,
	options agent.CreateHostOptions,
) (agent.Host, error) {
	dependencies, releaseConstruction, err := owner.beginConstruction(requestContext)
	if err != nil {
		return nil, err
	}
	defer releaseConstruction()
	if options.SessionID == "" {
		return nil, errors.New("agentloop: Agent Session id is empty")
	}
	if err = validateAgentOptions(options.AgentOptions); err != nil {
		return nil, err
	}
	identifier := options.SessionID
	conversation, err := dependencies.sessions.Prepare(
		&identifier,
		session.CreateOptions{
			Seed:     options.Seed,
			Metadata: options.Metadata,
		},
	)
	if err != nil {
		return nil, err
	}
	return owner.construct(
		requestContext,
		dependencies,
		conversation,
		options.AgentOptions,
		nil,
	)
}

// ResumeAgent constructs one unpublished Host from durable Session state.
func (owner *Factory) ResumeAgent(
	requestContext context.Context,
	options agent.ResumeHostOptions,
) (agent.Host, error) {
	dependencies, releaseConstruction, err := owner.beginConstruction(requestContext)
	if err != nil {
		return nil, err
	}
	defer releaseConstruction()
	if options.SessionID == "" {
		return nil, errors.New("agentloop: resume Session id is empty")
	}
	if err = validateAgentOptions(options.AgentOptions); err != nil {
		return nil, err
	}
	if dependencies.persistence == nil {
		return nil, errors.New(
			"agentloop: session persistence is not configured",
		)
	}
	preparation, err := dependencies.persistence.Prepare(
		requestContext,
		options.SessionID,
	)
	if err != nil {
		return nil, err
	}
	host, err := owner.construct(
		requestContext,
		dependencies,
		preparation.UnpublishedSession(),
		options.AgentOptions,
		preparation,
	)
	if err != nil {
		preparation.Dispose()
		return nil, err
	}
	return host, nil
}

func (owner *Factory) construct(
	requestContext context.Context,
	dependencies factoryDependencies,
	conversation session.Context,
	options agent.Options,
	preparation *session.Preparation,
) (agent.Host, error) {
	if conversation == nil {
		return nil, errors.New("agentloop: prepared Session is nil")
	}
	promptLayer := dependencies.prompts.NewLayer()
	if promptLayer == nil {
		return nil, errors.New("agentloop: System Prompt Layer is nil")
	}
	toolLayer, err := dependencies.toolLayers.NewLayer(
		requestContext,
		promptLayer,
	)
	if err != nil {
		return nil, errors.Join(err, promptLayer.Close(
			context.WithoutCancel(requestContext),
		))
	}
	agentScopeValue, err := newAgentScope(
		dependencies.events,
		dependencies.waterfalls,
		promptLayer,
		toolLayer,
	)
	if err != nil {
		return nil, errors.Join(
			err,
			toolLayer.Close(context.WithoutCancel(requestContext)),
			promptLayer.Close(context.WithoutCancel(requestContext)),
		)
	}
	subject, err := newReactLoopAgent(
		conversation,
		options,
		owner.maxParallelToolCalls,
		owner.reportObserverError,
		agentScopeValue,
		agentScopeValue,
	)
	if err != nil {
		return nil, errors.Join(
			err,
			agentScopeValue.Close(context.WithoutCancel(requestContext)),
		)
	}
	if err = subject.activate(
		requestContext,
		dependencies.sessions,
		dependencies.models,
		agentScopeValue.toolRuntime(),
		agentScopeValue.promptAssembler(),
	); err != nil {
		return nil, errors.Join(
			err,
			agentScopeValue.Close(context.WithoutCancel(requestContext)),
		)
	}
	if _, err = agentScopeValue.ApplySetup(
		requestContext,
		subject,
		newAgentVariablesSetup(options, conversation.Header()),
	); err != nil {
		return nil, errors.Join(
			err,
			agentScopeValue.Close(context.WithoutCancel(requestContext)),
		)
	}
	return newAgentHost(
		subject,
		agentScopeValue,
		dependencies.sessions,
		preparation,
	), nil
}

var _ agent.Factory = (*Factory)(nil)
