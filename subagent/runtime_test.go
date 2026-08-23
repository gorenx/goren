package subagent_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	subagentruntime "github.com/gorenx/goren/subagent/runtime"
)

type agentFixture struct {
	plugin.Base
	identifier   session.SessionID
	conversation *session.Session
}

func (subject *agentFixture) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "subagent-test-parent:" + string(subject.identifier),
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[agent.Agent](subject),
		},
	}
}

func (*agentFixture) Apply(context.Context) error   { return nil }
func (*agentFixture) Dispose(context.Context) error { return nil }
func (subject *agentFixture) ID() session.SessionID { return subject.identifier }
func (*agentFixture) OptionsValue() agent.Options   { return agent.Options{} }
func (subject *agentFixture) SessionValue() *session.Session {
	return subject.conversation
}
func (*agentFixture) InboxValue() *agent.Inbox                      { return nil }
func (*agentFixture) StatusValue() agent.Status                     { return agent.StatusIdle }
func (*agentFixture) Cancel(agent.CancelCause, agent.CancelOptions) {}
func (*agentFixture) WhenIdle(context.Context) error                { return nil }
func (*agentFixture) RunMaintenance(
	requestContext context.Context,
	task agent.MaintenanceTask,
) error {
	return task.Run(requestContext)
}
func (*agentFixture) Send(llm.UserMessage, agent.InboxTarget, bool) error {
	return nil
}
func (*agentFixture) Followup(llm.UserMessage) error { return nil }
func (*agentFixture) Steer(llm.UserMessage) error    { return nil }
func (*agentFixture) Inject(llm.UserMessage) error   { return nil }

type runOutcome struct {
	result subagent.Result
	err    error
}

type runFixture struct {
	identifier session.SessionID
	local      agent.Agent
	terminal   <-chan runOutcome
	disposed   chan struct{}
	once       sync.Once
}

func (running *runFixture) ID() session.SessionID {
	return running.identifier
}

func (running *runFixture) LocalAgent() (agent.Agent, bool) {
	return running.local, running.local != nil
}

func (running *runFixture) AwaitResult(
	waitContext context.Context,
) (subagent.Result, error) {
	select {
	case <-waitContext.Done():
		return subagent.Result{}, waitContext.Err()
	case outcome := <-running.terminal:
		return outcome.result, outcome.err
	}
}

func (running *runFixture) Dispose(context.Context) error {
	running.once.Do(func() {
		close(running.disposed)
	})
	return nil
}

type providerFixture struct {
	name         string
	capabilities subagent.Capabilities
	start        func(context.Context, subagent.ResolvedStartRequest) (subagent.Run, error)
}

func (candidate *providerFixture) Name() string {
	return candidate.name
}

func (candidate *providerFixture) Capabilities() subagent.Capabilities {
	return candidate.capabilities
}

func (*providerFixture) InheritsParentContext() bool {
	return false
}

func (candidate *providerFixture) Start(
	requestContext context.Context,
	requestSnapshot subagent.ResolvedStartRequest,
) (subagent.Run, error) {
	return candidate.start(requestContext, requestSnapshot)
}

type eventObserver struct {
	plugin.Base
	mutex    sync.Mutex
	facts    []plugin.Event
	addedErr error
	ended    chan subagent.Ended
}

func (*eventObserver) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "subagent-test-observer",
		Events: []plugin.EventSubscription{
			plugin.EventOf[subagent.ProviderAdded](),
			plugin.EventOf[subagent.ProviderRemoved](),
			plugin.EventOf[subagent.Started](),
			plugin.EventOf[subagent.Ended](),
		},
	}
}

func (*eventObserver) Apply(context.Context) error   { return nil }
func (*eventObserver) Dispose(context.Context) error { return nil }
func (observer *eventObserver) ObserveEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	observer.mutex.Lock()
	observer.facts = append(observer.facts, fact)
	observer.mutex.Unlock()
	switch notice := fact.(type) {
	case subagent.ProviderAdded:
		return observer.addedErr
	case subagent.Ended:
		if observer.ended != nil {
			observer.ended <- notice
		}
	}
	return nil
}

func (observer *eventObserver) snapshot() []plugin.Event {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()
	return append([]plugin.Event(nil), observer.facts...)
}

type eventFailureSink struct{}

func (eventFailureSink) ReportEventFailure(
	context.Context,
	plugin.EventFailure,
) {
}

type serviceConsumer struct {
	plugin.Base
	providers     subagent.ProviderRegistry
	oneShots      subagent.OneShotService
	continuations subagent.ContinuableService
	extensions    subagent.ExtensionRegistry
	catalog       subagent.Catalog
}

func (*serviceConsumer) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "subagent-test-consumer",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[subagent.ProviderRegistry](),
			plugin.ServiceOf[subagent.OneShotService](),
			plugin.ServiceOf[subagent.ContinuableService](),
			plugin.ServiceOf[subagent.ExtensionRegistry](),
			plugin.ServiceOf[subagent.Catalog](),
		},
	}
}

func (consumer *serviceConsumer) Apply(context.Context) error {
	var err error
	consumer.providers, err = plugin.Require[subagent.ProviderRegistry](consumer)
	if err != nil {
		return err
	}
	consumer.oneShots, err = plugin.Require[subagent.OneShotService](consumer)
	if err != nil {
		return err
	}
	consumer.continuations, err = plugin.Require[subagent.ContinuableService](consumer)
	if err != nil {
		return err
	}
	consumer.extensions, err = plugin.Require[subagent.ExtensionRegistry](consumer)
	if err != nil {
		return err
	}
	consumer.catalog, err = plugin.Require[subagent.Catalog](consumer)
	return err
}

func (consumer *serviceConsumer) Dispose(context.Context) error {
	consumer.providers = nil
	consumer.oneShots = nil
	consumer.continuations = nil
	consumer.extensions = nil
	consumer.catalog = nil
	return nil
}

type runtimeFixture struct {
	engine   *plugin.Runtime
	plugin   *subagentruntime.Plugin
	services *serviceConsumer
	parent   *agentFixture
	observer *eventObserver
}

func newRuntimeFixture(t *testing.T, observe bool) *runtimeFixture {
	t.Helper()
	conversation, err := session.New("parent", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	owner := subagentruntime.New(subagentruntime.RuntimeOptions{})
	services := &serviceConsumer{}
	parent := &agentFixture{
		identifier:   conversation.ID(),
		conversation: conversation,
	}
	instances := []plugin.Plugin{owner, services, parent}
	var observer *eventObserver
	settings := plugin.RuntimeSettings{}
	if observe {
		observer = &eventObserver{
			ended: make(chan subagent.Ended, 4),
		}
		instances = append(instances, observer)
		settings.EventFailures = eventFailureSink{}
	}
	engine := plugin.NewRuntime(settings)
	if _, err = engine.Start(context.Background(), instances...); err != nil {
		t.Fatal(err)
	}
	state := &runtimeFixture{
		engine:   engine,
		plugin:   owner,
		services: services,
		parent:   parent,
		observer: observer,
	}
	t.Cleanup(func() {
		if stopErr := engine.Shutdown(context.Background()); stopErr != nil {
			t.Errorf("shutdown: %v", stopErr)
		}
	})
	return state
}

func TestProviderRegistryOwnsOrderRollbackAndExactRemoval(t *testing.T) {
	state := newRuntimeFixture(t, true)
	alpha := &providerFixture{
		name: "alpha",
		start: func(context.Context, subagent.ResolvedStartRequest) (subagent.Run, error) {
			return nil, errors.New("unused")
		},
	}
	beta := &providerFixture{
		name: "beta",
		start: func(context.Context, subagent.ResolvedStartRequest) (subagent.Run, error) {
			return nil, errors.New("unused")
		},
	}
	alphaRegistration, err := state.services.providers.RegisterProvider(context.Background(), alpha)
	if err != nil {
		t.Fatal(err)
	}
	betaRegistration, err := state.services.providers.RegisterProvider(context.Background(), beta)
	if err != nil {
		t.Fatal(err)
	}
	if names := state.services.providers.ListProviders(); len(names) != 2 ||
		names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("provider order = %v", names)
	}
	if resolved, found := state.services.providers.GetProvider("alpha"); !found || resolved != alpha {
		t.Fatal("registry did not return the exact Provider")
	}
	if _, duplicateErr := state.services.providers.RegisterProvider(context.Background(), alpha); duplicateErr == nil {
		t.Fatal("duplicate Provider was accepted")
	}
	if err = alphaRegistration.Unregister(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = alphaRegistration.Unregister(context.Background()); err != nil {
		t.Fatalf("idempotent unregister: %v", err)
	}
	replacementRegistration, err := state.services.providers.RegisterProvider(context.Background(), alpha)
	if err != nil {
		t.Fatal(err)
	}
	if err = alphaRegistration.Unregister(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resolved, found := state.services.providers.GetProvider("alpha"); !found || resolved != alpha {
		t.Fatal("stale registration removed a later Provider")
	}
	if err = replacementRegistration.Unregister(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = betaRegistration.Unregister(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProviderAddedFailureRollsBackRegistration(t *testing.T) {
	state := newRuntimeFixture(t, true)
	sentinel := errors.New("provider rejected")
	state.observer.addedErr = sentinel
	candidate := &providerFixture{
		name: "rejected",
		start: func(context.Context, subagent.ResolvedStartRequest) (subagent.Run, error) {
			return nil, errors.New("unused")
		},
	}
	if _, err := state.services.providers.RegisterProvider(context.Background(), candidate); !errors.Is(err, sentinel) {
		t.Fatalf("register error = %v", err)
	}
	if _, found := state.services.providers.GetProvider("rejected"); found {
		t.Fatal("vetoed Provider remained visible")
	}
	if names := state.services.providers.ListProviders(); len(names) != 0 {
		t.Fatalf("provider order after rollback = %v", names)
	}
}

func TestOneShotStartSnapshotsRequestAndPairsLifecycle(t *testing.T) {
	state := newRuntimeFixture(t, true)
	terminal := make(chan runOutcome, 1)
	running := &runFixture{
		identifier: "child",
		terminal:   terminal,
		disposed:   make(chan struct{}),
	}
	var captured subagent.ResolvedStartRequest
	candidate := &providerFixture{
		name: "complete",
		capabilities: subagent.Capabilities{
			OutputSchema: true,
			DepthLimit:   true,
			ToolFilter:   true,
			Persona:      true,
		},
		start: func(
			_ context.Context,
			requestSnapshot subagent.ResolvedStartRequest,
		) (subagent.Run, error) {
			captured = requestSnapshot
			return running, nil
		},
	}
	registration, err := state.services.providers.RegisterProvider(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Unregister(context.Background())
	label := "review"
	persona := "critic"
	depth := int64(3)
	prompt := []llm.ContentBlock{
		llm.TextBlock{
			Type: "text",
			Text: "inspect",
		},
	}
	request := subagent.StartRequest{
		Label:        &label,
		Prompt:       prompt,
		Parent:       state.parent,
		OutputSchema: []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`),
		MaxDepth:     &depth,
		Persona:      &persona,
	}
	returned, err := state.services.oneShots.Start(context.Background(), "complete", request)
	if err != nil {
		t.Fatal(err)
	}
	if returned != running {
		t.Fatal("Start did not return the Provider's exact Run")
	}
	label = "mutated"
	persona = "mutated"
	depth = 99
	prompt[0] = llm.TextBlock{
		Type: "text",
		Text: "mutated",
	}
	if captured.Label == nil || *captured.Label != "review" ||
		captured.Persona == nil || *captured.Persona != "critic" ||
		captured.MaxDepth == nil || *captured.MaxDepth != 3 ||
		captured.Descriptor.Provider != "complete" ||
		captured.Descriptor.Mode != subagent.ModeOneShot {
		t.Fatalf("resolved request was not detached: %#v", captured)
	}
	capturedText := captured.Prompt[0].(llm.TextBlock)
	if capturedText.Text != "inspect" {
		t.Fatalf("captured prompt = %#v", capturedText)
	}
	terminal <- runOutcome{
		result: subagent.Result{
			Output: []llm.ContentBlock{
				llm.TextBlock{
					Type: "text",
					Text: "done",
				},
			},
			StopReason: subagent.StopCompleted,
		},
	}
	ended := <-state.observer.ended
	facts := state.observer.snapshot()
	var started subagent.Started
	for _, fact := range facts {
		if notice, matches := fact.(subagent.Started); matches {
			started = notice
			break
		}
	}
	if started.RunID == "" || ended.RunID != started.RunID ||
		ended.ID != started.ID || ended.Provider != started.Provider ||
		ended.StopReason != subagent.StopCompleted {
		t.Fatalf("lifecycle mismatch: start=%#v end=%#v", started, ended)
	}
}

func TestOneShotRejectsUnsupportedInputBeforeProviderStart(t *testing.T) {
	state := newRuntimeFixture(t, false)
	starts := 0
	candidate := &providerFixture{
		name: "weak",
		start: func(context.Context, subagent.ResolvedStartRequest) (subagent.Run, error) {
			starts++
			return nil, errors.New("must not start")
		},
	}
	registration, err := state.services.providers.RegisterProvider(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Unregister(context.Background())
	depth := int64(1)
	_, err = state.services.oneShots.Start(
		context.Background(),
		"weak",
		subagent.StartRequest{
			Parent:   state.parent,
			MaxDepth: &depth,
		},
	)
	var problem *subagent.Error
	if !errors.As(err, &problem) || problem.Code != subagent.ErrorUnsupportedCapability {
		t.Fatalf("Start error = %v", err)
	}
	if starts != 0 {
		t.Fatalf("Provider Start calls = %d", starts)
	}
}

func TestOneShotStartupFailureEmitsNoRunLifecycle(t *testing.T) {
	state := newRuntimeFixture(t, true)
	sentinel := errors.New("creation rolled back")
	candidate := &providerFixture{
		name: "failed",
		start: func(context.Context, subagent.ResolvedStartRequest) (subagent.Run, error) {
			return nil, sentinel
		},
	}
	registration, err := state.services.providers.RegisterProvider(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Unregister(context.Background())
	_, err = state.services.oneShots.Start(
		context.Background(),
		"failed",
		subagent.StartRequest{
			Parent: state.parent,
		},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Start error = %v", err)
	}
	for _, fact := range state.observer.snapshot() {
		switch fact.(type) {
		case subagent.Started, subagent.Ended:
			t.Fatalf("startup failure emitted lifecycle fact %#v", fact)
		}
	}
}

var _ agent.Agent = (*agentFixture)(nil)
var _ subagent.Provider = (*providerFixture)(nil)
var _ subagent.Run = (*runFixture)(nil)
var _ plugin.EventObserver = (*eventObserver)(nil)
var _ plugin.EventFailureReporter = eventFailureSink{}
