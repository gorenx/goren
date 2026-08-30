package agent_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

type registryAgent struct {
	id           session.SessionID
	conversation session.Context
}

func newRegistryAgent(identifier session.SessionID) *registryAgent {
	conversation, _ := session.New(identifier, session.CreateOptions{})
	return &registryAgent{
		id:           identifier,
		conversation: conversation,
	}
}

func (subject *registryAgent) ID() session.SessionID { return subject.id }
func (*registryAgent) OptionsValue() agent.Options   { return agent.Options{} }
func (subject *registryAgent) SessionValue() session.Context {
	return subject.conversation
}
func (*registryAgent) InboxValue() *agent.Inbox                      { return nil }
func (*registryAgent) StatusValue() agent.Status                     { return agent.StatusIdle }
func (*registryAgent) Cancel(agent.CancelCause, agent.CancelOptions) {}
func (*registryAgent) WhenIdle(context.Context) error                { return nil }
func (*registryAgent) RunMaintenance(
	requestContext context.Context,
	operation func(context.Context) error,
) error {
	return operation(requestContext)
}
func (*registryAgent) Followup(agentmessage.UserMessage) error { return nil }
func (*registryAgent) Steer(agentmessage.UserMessage) error    { return nil }
func (*registryAgent) Inject(agentmessage.UserMessage) error   { return nil }

type registryScope struct {
	mutex    sync.Mutex
	events   []agent.AgentEvent
	setups   int
	closes   int
	observer func(context.Context, agent.AgentEvent) error
}

func (scopeState *registryScope) ApplySetup(
	context.Context,
	agent.Agent,
	agent.Setup,
) (agent.ScopeResources, error) {
	scopeState.mutex.Lock()
	scopeState.setups++
	scopeState.mutex.Unlock()
	return emptyScopeResources{}, nil
}

func (scopeState *registryScope) Dispatch(
	requestContext context.Context,
	fact agent.AgentEvent,
) error {
	scopeState.mutex.Lock()
	scopeState.events = append(scopeState.events, fact)
	observer := scopeState.observer
	scopeState.mutex.Unlock()
	if observer != nil {
		return observer(requestContext, fact)
	}
	return nil
}

func (*registryScope) ResolvePreStep(
	requestContext context.Context,
	notice agent.PreStepNotice,
	action agent.PreStepAction,
) (agent.PreStepDecision, error) {
	return action.Execute(requestContext, notice)
}

func (*registryScope) ResolveRequest(
	requestContext context.Context,
	notice agent.RequestNotice,
	action agent.RequestAction,
) (agent.RequestResolution, error) {
	return action.Execute(requestContext, notice)
}

func (*registryScope) ResolveRequestError(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	action agent.RequestErrorHandler,
) (agent.RequestErrorAction, error) {
	return action.Execute(requestContext, notice)
}

func (scopeState *registryScope) Close(context.Context) error {
	scopeState.mutex.Lock()
	scopeState.closes++
	scopeState.mutex.Unlock()
	return nil
}

type emptyScopeResources struct{}

func (emptyScopeResources) Close(context.Context) error { return nil }

type registryHost struct {
	subject      *registryAgent
	scope        *registryScope
	closed       *[]session.SessionID
	done         chan struct{}
	once         sync.Once
	closeEntered chan struct{}
	closeRelease <-chan struct{}
}

func (host *registryHost) Agent() agent.Agent            { return host.subject }
func (host *registryHost) Scope() agent.Scope            { return host.scope }
func (*registryHost) EnterServing(context.Context) error { return nil }
func (*registryHost) Announce(context.Context) error     { return nil }
func (host *registryHost) Close(context.Context) error {
	host.once.Do(func() {
		if host.closeEntered != nil {
			close(host.closeEntered)
		}
		if host.closeRelease != nil {
			<-host.closeRelease
		}
		if host.closed != nil {
			*host.closed = append(*host.closed, host.subject.ID())
		}
		_ = host.scope.Close(context.Background())
		close(host.done)
	})
	return nil
}

type blockedRegistryFactory struct {
	host    *registryHost
	entered chan struct{}
}

func (factory *blockedRegistryFactory) CreateAgent(
	requestContext context.Context,
	options agent.CreateHostOptions,
) (agent.Host, error) {
	close(factory.entered)
	<-requestContext.Done()
	return factory.host, errors.New("test: rejected construction")
}

func (factory *blockedRegistryFactory) ResumeAgent(
	requestContext context.Context,
	options agent.ResumeHostOptions,
) (agent.Host, error) {
	return factory.CreateAgent(
		requestContext,
		agent.CreateHostOptions{
			SessionID:    options.SessionID,
			AgentOptions: options.AgentOptions,
		},
	)
}

type registryFactory struct {
	closed  *[]session.SessionID
	hosts   map[session.SessionID]*registryHost
	observe func(session.SessionID) func(context.Context, agent.AgentEvent) error
}

func (factory *registryFactory) create(identifier session.SessionID) agent.Host {
	scopeState := &registryScope{}
	if factory.observe != nil {
		scopeState.observer = factory.observe(identifier)
	}
	host := &registryHost{
		subject: newRegistryAgent(identifier),
		scope:   scopeState,
		closed:  factory.closed,
		done:    make(chan struct{}),
	}
	factory.hosts[identifier] = host
	return host
}

func (factory *registryFactory) CreateAgent(
	_ context.Context,
	options agent.CreateHostOptions,
) (agent.Host, error) {
	return factory.create(options.SessionID), nil
}

func (factory *registryFactory) ResumeAgent(
	_ context.Context,
	options agent.ResumeHostOptions,
) (agent.Host, error) {
	return factory.create(options.SessionID), nil
}

type registryFactoryPlugin struct {
	plugin.Base
	factory agent.Factory
}

func (provider *registryFactoryPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "test/agent-factory",
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[agent.Factory](provider.factory),
		},
	}
}
func (*registryFactoryPlugin) Apply(requestContext context.Context) error {
	return requestContext.Err()
}
func (*registryFactoryPlugin) Dispose(context.Context) error { return nil }

func startRegistry(
	t *testing.T,
	factory *registryFactory,
) (*plugin.Runtime, *agent.RegistryService) {
	t.Helper()
	service := agent.NewRegistry(agent.RegistryOptions{})
	adapter, err := agent.NewRegistryPlugin(service)
	if err != nil {
		t.Fatal(err)
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err = runtimeEngine.Start(
		context.Background(),
		&registryFactoryPlugin{
			factory: factory,
		},
		adapter,
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if shutdownErr := runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})
	return runtimeEngine, service
}

func TestRegistryPublishesAndRetiresExactAgent(t *testing.T) {
	t.Parallel()
	factory := &registryFactory{
		hosts: make(map[session.SessionID]*registryHost),
	}
	_, service := startRegistry(t, factory)
	handle, err := service.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "agent-1",
			Setup:     setupRecord{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if current, found := service.Get("agent-1"); !found || !agent.Same(current, handle.Subject) {
		t.Fatal("created Agent is not visible")
	}
	host := factory.hosts["agent-1"]
	if host.scope.setups != 1 || len(host.scope.events) != 2 {
		t.Fatalf("setups/events = %d/%d", host.scope.setups, len(host.scope.events))
	}
	if _, ok := host.scope.events[0].(agent.Created); !ok {
		t.Fatalf("first event = %T", host.scope.events[0])
	}
	if err = handle.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, found := service.Get("agent-1"); found {
		t.Fatal("disposed Agent remains visible")
	}
	if _, ok := host.scope.events[2].(agent.Disposed); !ok {
		t.Fatalf("last event = %T", host.scope.events[2])
	}
}

func TestRegistryClosesDescendantsBeforeParent(t *testing.T) {
	t.Parallel()
	closed := []session.SessionID{}
	factory := &registryFactory{
		closed: &closed,
		hosts:  make(map[session.SessionID]*registryHost),
	}
	_, service := startRegistry(t, factory)
	parent, err := service.Create(context.Background(), agent.CreateOptions{
		SessionID: "parent",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(context.Background(), agent.CreateOptions{
		SessionID:     "child",
		RuntimeParent: parent.Subject,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = parent.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(closed) != 2 || closed[0] != "child" || closed[1] != "parent" {
		t.Fatalf("close order = %v", closed)
	}
}

func TestStaleHandleCannotCloseReusedSessionID(t *testing.T) {
	t.Parallel()
	factory := &registryFactory{
		hosts: make(map[session.SessionID]*registryHost),
	}
	_, service := startRegistry(t, factory)
	first, err := service.Create(context.Background(), agent.CreateOptions{
		SessionID: "reused",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(context.Background(), agent.CreateOptions{
		SessionID: "reused",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if current, found := service.Get("reused"); !found || !agent.Same(current, second.Subject) {
		t.Fatal("stale Handle affected the successor Agent")
	}
}

func TestRegistryReactivatesAfterFactoryReplacement(t *testing.T) {
	firstFactory := &registryFactory{
		hosts: make(map[session.SessionID]*registryHost),
	}
	service := agent.NewRegistry(agent.RegistryOptions{})
	adapter, err := agent.NewRegistryPlugin(service)
	if err != nil {
		t.Fatal(err)
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(
		context.Background(),
		&registryFactoryPlugin{
			factory: firstFactory,
		},
		adapter,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if shutdownErr := runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})
	first, err := service.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "first-activation",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	secondFactory := &registryFactory{
		hosts: make(map[session.SessionID]*registryHost),
	}
	if err = runtimeEngine.Replace(
		context.Background(),
		handles[0],
		&registryFactoryPlugin{
			factory: secondFactory,
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, found := service.Get(first.Subject.ID()); found {
		t.Fatal("Factory replacement retained the prior activation Agent")
	}
	second, err := service.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "second-activation",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if current, found := service.Get(second.Subject.ID()); !found || !agent.Same(current, second.Subject) {
		t.Fatal("Registry did not admit an Agent after reactivation")
	}
}

func TestDisposedObserverMayDisposeSameAgent(t *testing.T) {
	var owned agent.Handle
	factory := &registryFactory{
		hosts: make(map[session.SessionID]*registryHost),
		observe: func(session.SessionID) func(context.Context, agent.AgentEvent) error {
			return func(
				requestContext context.Context,
				fact agent.AgentEvent,
			) error {
				if _, closing := fact.(agent.Disposed); closing {
					return owned.Dispose(context.Background())
				}
				return nil
			}
		},
	}
	_, service := startRegistry(t, factory)
	var err error
	owned, err = service.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "reentrant-dispose",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- owned.Dispose(context.Background())
	}()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reentrant Disposed observer deadlocked Agent disposal")
	}
}

func TestChildCreatedObserverMayDisposeParent(t *testing.T) {
	var parent agent.Handle
	factory := &registryFactory{
		hosts: make(map[session.SessionID]*registryHost),
		observe: func(identifier session.SessionID) func(
			context.Context,
			agent.AgentEvent,
		) error {
			if identifier != "child" {
				return nil
			}
			return func(
				requestContext context.Context,
				fact agent.AgentEvent,
			) error {
				if _, created := fact.(agent.Created); created {
					return parent.Dispose(context.Background())
				}
				return nil
			}
		},
	}
	_, service := startRegistry(t, factory)
	var err error
	parent, err = service.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "parent",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, createErr := service.Create(
			context.Background(),
			agent.CreateOptions{
				SessionID:     "child",
				RuntimeParent: parent.Subject,
			},
		)
		done <- createErr
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("child Created observer deadlocked parent disposal")
	}
	if err = parent.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(service.List()) != 0 {
		t.Fatal("parent disposal retained a child construction")
	}
}

func TestRegistryDeactivationWaitsForRejectedHostCleanup(t *testing.T) {
	releaseClose := make(chan struct{})
	closeEntered := make(chan struct{})
	scopeState := &registryScope{}
	blockedFactory := &blockedRegistryFactory{
		host: &registryHost{
			subject:      newRegistryAgent("blocked"),
			scope:        scopeState,
			done:         make(chan struct{}),
			closeEntered: closeEntered,
			closeRelease: releaseClose,
		},
		entered: make(chan struct{}),
	}
	service := agent.NewRegistry(agent.RegistryOptions{})
	adapter, err := agent.NewRegistryPlugin(service)
	if err != nil {
		t.Fatal(err)
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(
		context.Background(),
		&registryFactoryPlugin{
			factory: blockedFactory,
		},
		adapter,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if shutdownErr := runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})
	createDone := make(chan error, 1)
	go func() {
		_, createErr := service.Create(
			context.Background(),
			agent.CreateOptions{
				SessionID: "blocked",
			},
		)
		createDone <- createErr
	}()
	<-blockedFactory.entered
	deactivationDone := make(chan error, 1)
	go func() {
		deactivationDone <- runtimeEngine.Unload(
			context.Background(),
			handles[1],
		)
	}()
	<-closeEntered
	select {
	case err = <-deactivationDone:
		t.Fatalf("Registry deactivated before Host cleanup completed: %v", err)
	default:
	}
	close(releaseClose)
	if err = <-deactivationDone; err != nil {
		t.Fatal(err)
	}
	if err = <-createDone; err == nil {
		t.Fatal("canceled construction unexpectedly succeeded")
	}
	if scopeState.closes != 1 {
		t.Fatalf("Host closed Scope %d times, want 1", scopeState.closes)
	}
}

var _ agent.Agent = (*registryAgent)(nil)
var _ agent.Scope = (*registryScope)(nil)
var _ agent.Host = (*registryHost)(nil)
var _ agent.Factory = (*registryFactory)(nil)
var _ agent.Factory = (*blockedRegistryFactory)(nil)
var _ plugin.Plugin = (*registryFactoryPlugin)(nil)
