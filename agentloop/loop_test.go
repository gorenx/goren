package agentloop_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

type postCommitFailureSink struct{}

func (postCommitFailureSink) ReportPostCommitFailure(
	session.PostCommitFailure,
) {
}

type eventFailureSink struct{}

func (eventFailureSink) ReportEventFailure(
	context.Context,
	plugin.EventFailure,
) {
}

type scriptedAdapter struct {
	mutex     sync.Mutex
	responses [][]llm.StreamChunk
	requests  []llm.GenerateOptions
}

func (backend *scriptedAdapter) Stream(
	_ context.Context,
	requestOptions llm.GenerateOptions,
) (llm.ChunkStream, error) {
	backend.mutex.Lock()
	deferred, err := llm.CloneGenerateOptions(requestOptions)
	if err != nil {
		backend.mutex.Unlock()
		return nil, err
	}
	backend.requests = append(backend.requests, deferred)
	requestIndex := len(backend.requests) - 1
	if requestIndex >= len(backend.responses) {
		backend.mutex.Unlock()
		return nil, errors.New("test adapter has no scripted response")
	}
	response := backend.responses[requestIndex]
	backend.mutex.Unlock()
	return llm.NewSliceStream(response)
}

func (backend *scriptedAdapter) snapshots() []llm.GenerateOptions {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	result := make([]llm.GenerateOptions, 0, len(backend.requests))
	for _, requestSnapshot := range backend.requests {
		detached, _ := llm.CloneGenerateOptions(requestSnapshot)
		result = append(result, detached)
	}
	return result
}

type lifecycleObserver struct {
	plugin.Base
	mutex   sync.Mutex
	entries []string
}

func (*lifecycleObserver) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "agentloop-test-lifecycle-observer",
		Events: []plugin.EventSubscription{
			plugin.EventOf[session.Created](),
			plugin.EventOf[agent.Created](),
			plugin.EventOf[agent.SessionStarted](),
			plugin.EventOf[agent.Disposed](),
			plugin.EventOf[session.Disposed](),
		},
	}
}

func (*lifecycleObserver) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*lifecycleObserver) Dispose(context.Context) error {
	return nil
}

func (observerState *lifecycleObserver) ObserveEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	entry := ""
	switch fact.(type) {
	case session.Created:
		entry = session.CreatedEventName
	case agent.Created:
		entry = agent.CreatedEventName
	case agent.SessionStarted:
		entry = agent.SessionStartEventName
	case agent.Disposed:
		entry = agent.DisposedEventName
	case session.Disposed:
		entry = session.DisposedEventName
	}
	if entry == "" {
		return nil
	}
	observerState.mutex.Lock()
	observerState.entries = append(observerState.entries, entry)
	observerState.mutex.Unlock()
	return nil
}

func (observerState *lifecycleObserver) snapshot() []string {
	observerState.mutex.Lock()
	defer observerState.mutex.Unlock()
	return append([]string(nil), observerState.entries...)
}

type harnessFixture struct {
	runtimeEngine *plugin.Runtime
	agents        *agent.RegistryPlugin
	sessions      *session.MemoryStore
	models        *llm.Runtime
	toolCatalog   tools.ToolCatalog
	loopPlugin    *agentloop.Plugin
	backend       *scriptedAdapter
	lifecycle     *lifecycleObserver
}

func newHarnessFixture(
	t *testing.T,
	responses [][]llm.StreamChunk,
) *harnessFixture {
	return newHarnessFixtureWithSettings(
		t,
		responses,
		agentloop.Settings{
			MaxParallelToolCalls: agentloop.DefaultMaxParallelToolCalls,
		},
	)
}

func newHarnessFixtureWithSettings(
	t *testing.T,
	responses [][]llm.StreamChunk,
	loopSettings agentloop.Settings,
	rootExtensions ...plugin.Plugin,
) *harnessFixture {
	t.Helper()
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		t.Fatal(err)
	}
	toolSettings, err := tools.ValidateConfig(tools.Config{})
	if err != nil {
		t.Fatal(err)
	}
	loopPlugin, err := agentloop.New(
		loopSettings,
		agentloop.RuntimeOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore, err := session.NewMemoryStore(session.MemoryStoreOptions{
		PostCommitFailures: postCommitFailureSink{},
	})
	if err != nil {
		t.Fatal(err)
	}
	agentRegistry := agent.NewRegistry(agent.RegistryOptions{})
	modelRuntime := llm.NewRuntime(nil)
	promptRuntime := systemprompt.New(
		promptSettings,
		systemprompt.RegistryOptions{},
	)
	toolService := tools.New(toolSettings)
	lifecycle := &lifecycleObserver{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: eventFailureSink{},
	})
	rootPlugins := []plugin.Plugin{
		lifecycle,
		agentRegistry,
		sessionStore,
		modelRuntime,
		promptRuntime,
		toolService,
	}
	rootPlugins = append(rootPlugins, rootExtensions...)
	rootPlugins = append(rootPlugins, loopPlugin)
	if _, err = runtimeEngine.Start(
		context.Background(),
		rootPlugins...,
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if shutdownErr := runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})
	backend := &scriptedAdapter{
		responses: responses,
	}
	adapterHandle, err := modelRuntime.RegisterAdapter(
		context.Background(),
		[]string{"mock"},
		backend,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if releaseErr := adapterHandle.Release(context.Background()); releaseErr != nil {
			t.Error(releaseErr)
		}
	})
	return &harnessFixture{
		runtimeEngine: runtimeEngine,
		agents:        agentRegistry,
		sessions:      sessionStore,
		models:        modelRuntime,
		toolCatalog:   toolService,
		loopPlugin:    loopPlugin,
		backend:       backend,
		lifecycle:     lifecycle,
	}
}

func TestLoopPublishesDrivesAndDisposesOneAgentLifecycle(t *testing.T) {
	state := newHarnessFixture(t, modelResponses())
	registerEchoTool(t, state)
	handleState, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "loop-e2e",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if handleState.Subject.ID() != "loop-e2e" ||
		handleState.Subject.SessionValue().ID() != "loop-e2e" {
		t.Fatalf(
			"Agent/Session identity diverged: %q / %q",
			handleState.Subject.ID(),
			handleState.Subject.SessionValue().ID(),
		)
	}
	if err = handleState.Subject.Followup(userMessage(t, "hello")); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer cancelWait()
	if err = handleState.Subject.WhenIdle(waitContext); err != nil {
		t.Fatal(err)
	}
	requests := state.backend.snapshots()
	if len(requests) != 2 {
		t.Fatalf("model request count = %d, want 2", len(requests))
	}
	if len(requests[0].Tools) != 1 || requests[0].Tools[0].Name != "echo" {
		t.Fatalf("first request tools = %#v", requests[0].Tools)
	}
	if got := messageTexts(requests[1].Messages); !reflect.DeepEqual(
		got,
		[]string{
			"hello",
			"tool-call:echo",
			`{"value":"hello"}`,
			"tool-context",
		},
	) {
		t.Fatalf("second request messages = %#v", got)
	}
	wantPublished := []string{
		session.CreatedEventName,
		agent.CreatedEventName,
		agent.SessionStartEventName,
	}
	if got := state.lifecycle.snapshot(); !reflect.DeepEqual(got, wantPublished) {
		t.Fatalf("publication lifecycle = %#v, want %#v", got, wantPublished)
	}
	if err = handleState.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, found := state.agents.Get("loop-e2e"); found {
		t.Fatal("disposed Agent remains registered")
	}
	if _, found := state.sessions.Get("loop-e2e"); found {
		t.Fatal("disposed Session remains registered")
	}
	wantComplete := append(
		wantPublished,
		agent.DisposedEventName,
		session.DisposedEventName,
	)
	if got := state.lifecycle.snapshot(); !reflect.DeepEqual(got, wantComplete) {
		t.Fatalf("complete lifecycle = %#v, want %#v", got, wantComplete)
	}
}

func TestConfiguredAgentsStartAfterRuntimeAndOwnDetachedMetadata(t *testing.T) {
	createdAt := int64(42)
	workingDirectory := t.TempDir()
	expectedWorkingDirectory := workingDirectory
	parentSession := session.SessionID("parent-session")
	seedLength := int64(0)
	delegationDepth := int64(1)
	agentPreset := "default"
	state := newHarnessFixtureWithSettings(
		t,
		nil,
		agentloop.Settings{
			MaxParallelToolCalls: agentloop.DefaultMaxParallelToolCalls,
			StartupAgents: []agentloop.StartupAgent{
				{
					Label:     "configured",
					SessionID: "configured-session",
					Metadata: session.Metadata{
						CreatedAt:       &createdAt,
						CWD:             &workingDirectory,
						ParentSession:   &parentSession,
						SeedLength:      &seedLength,
						Origin:          session.OriginSubagent,
						DelegationDepth: &delegationDepth,
						AgentPreset:     &agentPreset,
					},
				},
			},
		},
	)
	createdAt = 99
	workingDirectory = "/mutated"
	parentSession = "mutated-parent"
	seedLength = 99
	delegationDepth = 99
	agentPreset = "mutated"
	if _, found := state.agents.Get("configured-session"); found {
		t.Fatal("configured Agent started before Runtime.Start returned")
	}
	handles, err := state.loopPlugin.StartConfiguredAgents(
		context.Background(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(handles) != 1 {
		t.Fatalf("configured Agent count = %d, want 1", len(handles))
	}
	defer func() {
		if disposeErr := handles[0].Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	}()
	header := handles[0].Subject.SessionValue().Header()
	if header.CreatedAt != 42 || header.CWD == nil ||
		*header.CWD != expectedWorkingDirectory || header.ParentSession == nil ||
		*header.ParentSession != "parent-session" ||
		header.SeedLength == nil || *header.SeedLength != 0 ||
		header.DelegationDepth == nil || *header.DelegationDepth != 1 ||
		header.AgentPreset == nil || *header.AgentPreset != "default" {
		t.Fatalf("configured Session header = %#v", header)
	}
	if _, err = state.loopPlugin.StartConfiguredAgents(
		context.Background(),
	); err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("second configured startup error = %v", err)
	}
}

func TestConfiguredAgentFailureRollsBackEarlierStartup(t *testing.T) {
	state := newHarnessFixtureWithSettings(
		t,
		nil,
		agentloop.Settings{
			MaxParallelToolCalls: agentloop.DefaultMaxParallelToolCalls,
			StartupAgents: []agentloop.StartupAgent{
				{
					Label:     "first",
					SessionID: "configured-first",
				},
				{
					Label:     "collision",
					SessionID: "already-live",
				},
			},
		},
	)
	existing := createTestAgent(t, state, "already-live")
	defer func() {
		if disposeErr := existing.Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	}()
	_, err := state.loopPlugin.StartConfiguredAgents(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already-live") {
		t.Fatalf("configured startup error = %v", err)
	}
	if _, found := state.agents.Get("configured-first"); found {
		t.Fatal("earlier configured Agent survived transaction rollback")
	}
	if _, found := state.sessions.Get("configured-first"); found {
		t.Fatal("earlier configured Session survived transaction rollback")
	}
	if retained, found := state.agents.Get("already-live"); !found || retained != existing.Subject {
		t.Fatal("startup rollback removed the pre-existing Agent")
	}
}

type requestExtension struct {
	plugin.Base
	subject  agent.Agent
	disposed bool
}

type prePublicationExtension struct {
	plugin.Base
	sendErr error
}

func (*prePublicationExtension) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "agentloop-test-pre-publication-extension",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[agent.Agent](),
		},
	}
}

func (extension *prePublicationExtension) Apply(
	requestContext context.Context,
) error {
	subject, err := plugin.Require[agent.Agent](extension)
	if err != nil {
		return err
	}
	extension.sendErr = subject.Followup(userMessageValue("too early"))
	return requestContext.Err()
}

func (*prePublicationExtension) Dispose(context.Context) error {
	return nil
}

func TestAgentRejectsWorkBeforeCommitPublication(t *testing.T) {
	state := newHarnessFixture(t, nil)
	extension := &prePublicationExtension{}
	handleState, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "pre-publication-admission",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
			Provisioner: agent.MountPlugins(extension),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if extension.sendErr == nil ||
		!strings.Contains(extension.sendErr.Error(), "not live") {
		t.Fatalf("pre-publication Followup error = %v", extension.sendErr)
	}
	if handleState.Subject.InboxValue().HasPending() {
		t.Fatal("pre-publication Followup mutated the Agent Inbox")
	}
	if err = handleState.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (extension *requestExtension) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "agentloop-test-request-extension",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[agent.Agent](),
		},
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf(extension),
		},
	}
}

func (extension *requestExtension) Apply(
	requestContext context.Context,
) error {
	subject, err := plugin.Require[agent.Agent](extension)
	if err != nil {
		return err
	}
	extension.subject = subject
	return requestContext.Err()
}

func (extension *requestExtension) Dispose(context.Context) error {
	extension.disposed = true
	extension.subject = nil
	return nil
}

func (*requestExtension) Intercept(
	requestContext context.Context,
	notice agent.RequestNotice,
	downstream plugin.WaterfallAction[
		agent.RequestNotice,
		agent.RequestResolution,
	],
) (agent.RequestResolution, error) {
	resolved, err := downstream.Execute(requestContext, notice)
	if err != nil {
		return agent.RequestResolution{}, err
	}
	resolved.Config.Model = "extension-model"
	return resolved, nil
}

func TestAgentExtensionResolvesExactAgentAndWaterfall(t *testing.T) {
	state := newHarnessFixture(t, nil)
	extension := &requestExtension{}
	handleState, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "extension-agent",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "base-model",
			},
			Provisioner: agent.MountPlugins(extension),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if extension.subject != handleState.Subject {
		t.Fatal("extension did not resolve the exact scoped Agent Service")
	}
	resolved, err := agent.ResolveRequest(
		context.Background(),
		agent.RequestNotice{
			Subject: handleState.Subject,
			Turn:    1,
			Step:    1,
		},
		agent.RequestActionFunc(func(
			context.Context,
			agent.RequestNotice,
		) (agent.RequestResolution, error) {
			return agent.RequestResolution{
				Config: llm.CallConfig{
					Provider: "mock",
					Model:    "base-model",
				},
			}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Model != "extension-model" {
		t.Fatalf("resolved model = %q", resolved.Model)
	}
	if err = handleState.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !extension.disposed || extension.subject != nil {
		t.Fatal("extension lifecycle was not released with its Agent")
	}
}

type toolExecutionExtension struct {
	plugin.Base
	invocations atomic.Int64
}

func (extension *toolExecutionExtension) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "agentloop-test-tool-execution-extension",
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf(extension),
		},
	}
}

func (*toolExecutionExtension) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*toolExecutionExtension) Dispose(context.Context) error {
	return nil
}

func (extension *toolExecutionExtension) Intercept(
	requestContext context.Context,
	request tools.ExecuteRequest,
	downstream plugin.WaterfallAction[
		tools.ExecuteRequest,
		tools.ExecuteOutcome,
	],
) (tools.ExecuteOutcome, error) {
	extension.invocations.Add(1)
	return downstream.Execute(requestContext, request)
}

func TestAgentExtensionInterceptsItsScopedToolRuntime(t *testing.T) {
	state := newHarnessFixture(t, modelResponses())
	registerEchoTool(t, state)
	extension := &toolExecutionExtension{}
	handleState, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "scoped-tool-extension",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
			Provisioner: agent.MountPlugins(extension),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = handleState.Subject.Followup(userMessage(t, "use tool")); err != nil {
		t.Fatal(err)
	}
	waitForIdle(t, handleState.Subject)
	if got := extension.invocations.Load(); got != 1 {
		t.Fatalf("scoped Tool execution interception count = %d, want 1", got)
	}
	if err = handleState.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type failingAgentExtension struct {
	plugin.Base
	applied  bool
	disposed bool
}

func (*failingAgentExtension) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "agentloop-test-failing-extension",
	}
}

func (extension *failingAgentExtension) Apply(context.Context) error {
	extension.applied = true
	return errors.New("extension activation failed")
}

func (extension *failingAgentExtension) Dispose(context.Context) error {
	extension.disposed = true
	return nil
}

func TestFailedExtensionNeverPublishesPartialAgentTree(t *testing.T) {
	state := newHarnessFixture(t, nil)
	baselineStatuses := len(state.runtimeEngine.Statuses())
	extension := &failingAgentExtension{}
	_, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "failed-extension-agent",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
			Provisioner: agent.MountPlugins(extension),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "extension activation failed") {
		t.Fatalf("Create error = %v", err)
	}
	if !extension.applied || !extension.disposed {
		t.Fatalf(
			"extension lifecycle = Apply:%t Dispose:%t",
			extension.applied,
			extension.disposed,
		)
	}
	if _, found := state.agents.Get("failed-extension-agent"); found {
		t.Fatal("failed Agent became visible in Registry")
	}
	if _, found := state.sessions.Get("failed-extension-agent"); found {
		t.Fatal("failed Session became visible in LiveStore")
	}
	if got := state.lifecycle.snapshot(); len(got) != 0 {
		t.Fatalf("failed tree published lifecycle Events: %#v", got)
	}
	if got := len(state.runtimeEngine.Statuses()); got != baselineStatuses {
		t.Fatalf("Runtime retained %d additional tree nodes", got-baselineStatuses)
	}
}

func TestRuntimeShutdownRetiresLiveAgentBeforeRootServices(t *testing.T) {
	state := newHarnessFixture(t, nil)
	handleState, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "shutdown-agent",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = state.runtimeEngine.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, found := state.agents.Get("shutdown-agent"); found {
		t.Fatal("Runtime shutdown left the Agent registered")
	}
	if _, found := state.sessions.Get("shutdown-agent"); found {
		t.Fatal("Runtime shutdown left the Session registered")
	}
	if err = handleState.Dispose(context.Background()); err != nil {
		t.Fatalf("post-shutdown Handle disposal is not idempotent: %v", err)
	}
	if _, err = state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "after-shutdown",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
		},
	); err == nil || !strings.Contains(err.Error(), "no Agent factory") {
		t.Fatalf("post-shutdown Create error = %v", err)
	}
	want := []string{
		session.CreatedEventName,
		agent.CreatedEventName,
		agent.SessionStartEventName,
		agent.DisposedEventName,
		session.DisposedEventName,
	}
	if got := state.lifecycle.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown lifecycle = %#v, want %#v", got, want)
	}
}

type flushBarrier struct {
	plugin.Base
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*flushBarrier) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "agentloop-test-flush-barrier",
		Events: []plugin.EventSubscription{
			plugin.EventOf[session.FlushRequested](),
		},
	}
}

func (*flushBarrier) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*flushBarrier) Dispose(context.Context) error {
	return nil
}

func (barrier *flushBarrier) ObserveEvent(
	requestContext context.Context,
	fact plugin.Event,
) error {
	if _, matches := fact.(session.FlushRequested); !matches {
		return nil
	}
	barrier.once.Do(func() {
		close(barrier.started)
	})
	select {
	case <-barrier.release:
		return nil
	case <-requestContext.Done():
		return context.Cause(requestContext)
	}
}

func TestTurnFlushCompletesBeforeAgentBecomesIdle(t *testing.T) {
	barrier := &flushBarrier{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	state := newHarnessFixtureWithSettings(
		t,
		[][]llm.StreamChunk{
			{
				llm.BlockEndChunk{
					Index: 0,
					Block: llm.NewTextBlock("done"),
				},
				llm.FinishChunk{
					Reason: llm.StopFinish{},
				},
			},
		},
		agentloop.Settings{
			MaxParallelToolCalls: agentloop.DefaultMaxParallelToolCalls,
		},
		barrier,
	)
	handleState, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "flush-before-idle",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			close(barrier.release)
		}
		if disposeErr := handleState.Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	}()
	if err = handleState.Subject.Followup(userMessage(t, "flush")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-barrier.started:
	case <-time.After(time.Second):
		t.Fatal("Session flush did not start")
	}
	idle := make(chan error, 1)
	go func() {
		idle <- handleState.Subject.WhenIdle(context.Background())
	}()
	select {
	case idleErr := <-idle:
		t.Fatalf("Agent became idle before flush completed: %v", idleErr)
	case <-time.After(20 * time.Millisecond):
	}
	close(barrier.release)
	released = true
	select {
	case idleErr := <-idle:
		if idleErr != nil {
			t.Fatal(idleErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Agent did not become idle after flush completed")
	}
	if ending := lastTurnEnd(t, handleState.Subject.SessionValue()); ending.Kind != "completed" {
		t.Fatalf("turn ending = %#v", ending)
	}
}

type parallelToolControl struct {
	firstStarted  chan struct{}
	secondSettled chan struct{}
	releaseFirst  chan struct{}
	mutex         sync.Mutex
	settled       []llm.CallID
}

func (control *parallelToolControl) run(
	_ json.RawMessage,
	runContext tools.ToolRunContext,
) (json.RawMessage, error) {
	callID := runContext.Execution.CallID
	switch callID {
	case "call-1":
		close(control.firstStarted)
		<-control.releaseFirst
	case "call-2":
		close(control.secondSettled)
	}
	control.mutex.Lock()
	control.settled = append(control.settled, callID)
	control.mutex.Unlock()
	runContext.DeferContext(userMessageValue("context-" + string(callID)))
	return json.RawMessage(`{"callId":"` + string(callID) + `"}`), nil
}

func (control *parallelToolControl) settledOrder() []llm.CallID {
	control.mutex.Lock()
	defer control.mutex.Unlock()
	return append([]llm.CallID(nil), control.settled...)
}

func TestParallelToolBodiesCommitResultsAndContextInModelOrder(t *testing.T) {
	state := newHarnessFixture(t, [][]llm.StreamChunk{
		toolCallResponse("call-1", "call-2"),
		{
			llm.BlockEndChunk{
				Index: 0,
				Block: llm.NewTextBlock("done"),
			},
			llm.FinishChunk{
				Reason: llm.StopFinish{},
			},
		},
	})
	control := &parallelToolControl{
		firstStarted:  make(chan struct{}),
		secondSettled: make(chan struct{}),
		releaseFirst:  make(chan struct{}),
	}
	var releaseFirst sync.Once
	defer releaseFirst.Do(func() {
		close(control.releaseFirst)
	})
	registerParallelTool(t, state, control.run)
	handleState := createTestAgent(t, state, "parallel-order")
	defer func() {
		if disposeErr := handleState.Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	}()
	if err := handleState.Subject.Followup(userMessage(t, "parallel")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-control.secondSettled:
	case <-time.After(time.Second):
		t.Fatal("second Tool body did not settle")
	}
	select {
	case <-control.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first Tool body was not started")
	}
	releaseFirst.Do(func() {
		close(control.releaseFirst)
	})
	waitForIdle(t, handleState.Subject)
	if settled := control.settledOrder(); !reflect.DeepEqual(
		settled,
		[]llm.CallID{"call-2", "call-1"},
	) {
		t.Fatalf("Tool body settlement = %#v", settled)
	}
	requests := state.backend.snapshots()
	if len(requests) != 2 {
		t.Fatalf("model request count = %d", len(requests))
	}
	wantTail := []string{
		`{"callId":"call-1"}`,
		`{"callId":"call-2"}`,
		"context-call-1",
		"context-call-2",
	}
	got := messageTexts(requests[1].Messages)
	if len(got) < len(wantTail) || !reflect.DeepEqual(
		got[len(got)-len(wantTail):],
		wantTail,
	) {
		t.Fatalf("ordered result/context tail = %#v, want %#v", got, wantTail)
	}
}

func TestMaintenanceWakeKeepsWhenIdleBehindSuccessorTurn(t *testing.T) {
	state := newHarnessFixture(t, [][]llm.StreamChunk{
		{
			llm.BlockEndChunk{
				Index: 0,
				Block: llm.NewTextBlock("done"),
			},
			llm.FinishChunk{
				Reason: llm.StopFinish{},
			},
		},
	})
	handleState := createTestAgent(t, state, "maintenance-wake")
	defer func() {
		if disposeErr := handleState.Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	}()
	maintenanceStarted := make(chan struct{})
	releaseMaintenance := make(chan struct{})
	maintenanceDone := make(chan error, 1)
	go func() {
		maintenanceDone <- handleState.Subject.RunMaintenance(
			context.Background(),
			func(context.Context) error {
				close(maintenanceStarted)
				<-releaseMaintenance
				return nil
			},
		)
	}()
	select {
	case <-maintenanceStarted:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not start")
	}
	if err := handleState.Subject.Followup(userMessage(t, "queued")); err != nil {
		t.Fatal(err)
	}
	idle := make(chan error, 1)
	go func() {
		idle <- handleState.Subject.WhenIdle(context.Background())
	}()
	if requests := state.backend.snapshots(); len(requests) != 0 {
		t.Fatalf("model ran during maintenance: %d request(s)", len(requests))
	}
	close(releaseMaintenance)
	if err := <-maintenanceDone; err != nil {
		t.Fatal(err)
	}
	select {
	case idleErr := <-idle:
		if idleErr != nil {
			t.Fatal(idleErr)
		}
	case <-time.After(time.Second):
		t.Fatal("WhenIdle did not include the latched successor Turn")
	}
	if requests := state.backend.snapshots(); len(requests) != 1 {
		t.Fatalf("model request count = %d, want 1", len(requests))
	}
}

func TestFirstCancelCauseSurvivesDisposal(t *testing.T) {
	state := newHarnessFixture(t, [][]llm.StreamChunk{
		toolCallResponse("call-1"),
	})
	toolStarted := make(chan struct{})
	registerParallelTool(t, state, func(
		_ json.RawMessage,
		runContext tools.ToolRunContext,
	) (json.RawMessage, error) {
		close(toolStarted)
		<-runContext.Context.Done()
		return json.RawMessage(`{"cancelled":true}`), nil
	})
	handleState := createTestAgent(t, state, "cancel-dispose")
	if err := handleState.Subject.Followup(userMessage(t, "cancel")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-toolStarted:
	case <-time.After(time.Second):
		t.Fatal("Tool body did not start")
	}
	handleState.Subject.Cancel(
		agent.HookCancel{
			Reason: "first-cause",
		},
		agent.CancelOptions{
			KeepInbox: true,
		},
	)
	if err := handleState.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	ending := lastTurnEnd(t, handleState.Subject.SessionValue())
	if ending.Kind != "aborted" || ending.Reason == nil ||
		ending.Reason.Kind != "hook" || ending.Reason.Reason != "first-cause" {
		t.Fatalf("turn ending = %#v", ending)
	}
}

type externalCancelCause struct{}

func (externalCancelCause) CancelKind() string { return "external-extension" }

func TestUnknownCancelCauseUsesHookReason(t *testing.T) {
	state := newHarnessFixture(t, [][]llm.StreamChunk{
		toolCallResponse("call-unknown-cause"),
	})
	toolStarted := make(chan struct{})
	registerParallelTool(t, state, func(
		_ json.RawMessage,
		runContext tools.ToolRunContext,
	) (json.RawMessage, error) {
		close(toolStarted)
		<-runContext.Context.Done()
		return nil, runContext.Context.Err()
	})
	handleState := createTestAgent(t, state, "cancel-unknown-cause")
	if err := handleState.Subject.Followup(userMessage(t, "cancel")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-toolStarted:
	case <-time.After(time.Second):
		t.Fatal("Tool body did not start")
	}
	handleState.Subject.Cancel(externalCancelCause{}, agent.CancelOptions{
		KeepInbox: true,
	})
	if err := handleState.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	ending := lastTurnEnd(t, handleState.Subject.SessionValue())
	if ending.Kind != "aborted" || ending.Reason == nil ||
		ending.Reason.Kind != "hook" ||
		ending.Reason.Reason != "external-extension" {
		t.Fatalf("turn ending = %#v", ending)
	}
}

type retryExtension struct {
	plugin.Base
	notices chan agent.RequestErrorNotice
}

func (extension *retryExtension) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "agentloop-test-retry-extension",
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf(extension),
		},
	}
}

func (*retryExtension) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*retryExtension) Dispose(context.Context) error {
	return nil
}

func (extension *retryExtension) Intercept(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	downstream plugin.WaterfallAction[
		agent.RequestErrorNotice,
		agent.RequestErrorAction,
	],
) (agent.RequestErrorAction, error) {
	extension.notices <- notice
	action, err := downstream.Execute(requestContext, notice)
	if err != nil {
		return agent.RequestErrorAction{}, err
	}
	action.Retry = true
	return action, requestContext.Err()
}

func TestRequestErrorRetryRepeatsAttemptInsideOneStep(t *testing.T) {
	state := newHarnessFixture(t, [][]llm.StreamChunk{
		{
			llm.FinishChunk{
				Reason: llm.ErrorFinish{
					Failure: llm.LlmFailure{
						Message: "temporary",
						Code:    "UPSTREAM",
					},
				},
			},
		},
		{
			llm.BlockEndChunk{
				Index: 0,
				Block: llm.NewTextBlock("recovered"),
			},
			llm.FinishChunk{
				Reason: llm.StopFinish{},
			},
		},
	})
	extension := &retryExtension{
		notices: make(chan agent.RequestErrorNotice, 1),
	}
	handleState, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "request-retry",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
			Provisioner: agent.MountPlugins(extension),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if disposeErr := handleState.Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	}()
	if err = handleState.Subject.Followup(userMessage(t, "retry")); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer cancelWait()
	if err = handleState.Subject.WhenIdle(waitContext); err != nil {
		t.Fatal(err)
	}
	if requests := state.backend.snapshots(); len(requests) != 2 {
		t.Fatalf("model request count = %d, want 2 attempts", len(requests))
	}
	select {
	case notice := <-extension.notices:
		if notice.Turn != 1 || notice.Step != 1 ||
			notice.Provider != "mock" || notice.Failure.Code != "UPSTREAM" {
			t.Fatalf("request-error notice = %#v", notice)
		}
	default:
		t.Fatal("request-error extension was not invoked")
	}
	ending := lastTurnEnd(t, handleState.Subject.SessionValue())
	if ending.Kind != "completed" {
		t.Fatalf("turn ending = %#v, want completed", ending)
	}
}

func registerEchoTool(t *testing.T, state *harnessFixture) {
	t.Helper()
	deferredContext := userMessage(t, "tool-context")
	_, err := state.toolCatalog.AddTool(
		context.Background(),
		tools.ToolDefinition{
			Name:        "echo",
			Description: "echo one object",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Output: tools.ToolOutputDefinition{
				Schema: json.RawMessage(`{"type":"object"}`),
				Renderer: tools.OutputRendererFunc(func(
					_ json.RawMessage,
					value json.RawMessage,
				) ([]llm.ContentBlock, error) {
					return []llm.ContentBlock{
						llm.NewTextBlock(string(value)),
					}, nil
				}),
			},
			Executor: tools.ExecutorFunc(func(
				arguments json.RawMessage,
				runContext tools.ToolRunContext,
			) (json.RawMessage, error) {
				runContext.DeferContext(deferredContext)
				return append(json.RawMessage(nil), arguments...), nil
			}),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func modelResponses() [][]llm.StreamChunk {
	return [][]llm.StreamChunk{
		{
			llm.BlockEndChunk{
				Index: 0,
				Block: llm.ToolCallBlock{
					ID:        "call-1",
					Name:      "echo",
					Arguments: `{"value":"hello"}`,
				},
			},
			llm.FinishChunk{
				Reason: llm.ToolCallsFinish{},
			},
		},
		{
			llm.BlockEndChunk{
				Index: 0,
				Block: llm.NewTextBlock("done"),
			},
			llm.FinishChunk{
				Reason: llm.StopFinish{},
			},
		},
	}
}

func userMessage(t *testing.T, content string) llm.UserMessage {
	t.Helper()
	message, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{
			llm.NewTextBlock(content),
		},
		Source: llm.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

type turnEndObservation struct {
	Kind   string `json:"kind"`
	Reason *struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason,omitempty"`
	} `json:"reason,omitempty"`
}

func createTestAgent(
	t *testing.T,
	state *harnessFixture,
	identifier session.SessionID,
) agent.Handle {
	t.Helper()
	handleState, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: identifier,
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return handleState
}

func waitForIdle(t *testing.T, subject agent.Agent) {
	t.Helper()
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer cancelWait()
	if err := subject.WhenIdle(waitContext); err != nil {
		t.Fatal(err)
	}
}

func userMessageValue(content string) llm.UserMessage {
	message, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{
			llm.NewTextBlock(content),
		},
		Source: llm.UserMessageSource{},
	})
	if err != nil {
		panic(err)
	}
	return message
}

func toolCallResponse(callIDs ...llm.CallID) []llm.StreamChunk {
	response := make([]llm.StreamChunk, 0, len(callIDs)+1)
	for index, callID := range callIDs {
		response = append(
			response,
			llm.BlockEndChunk{
				Index: index,
				Block: llm.ToolCallBlock{
					ID:        callID,
					Name:      "parallel",
					Arguments: `{"value":true}`,
				},
			},
		)
	}
	return append(
		response,
		llm.FinishChunk{
			Reason: llm.ToolCallsFinish{},
		},
	)
}

func registerParallelTool(
	t *testing.T,
	state *harnessFixture,
	toolBody func(
		json.RawMessage,
		tools.ToolRunContext,
	) (json.RawMessage, error),
) {
	t.Helper()
	_, err := state.toolCatalog.AddTool(
		context.Background(),
		tools.ToolDefinition{
			Name:        "parallel",
			Description: "test parallel scheduling",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Output: tools.ToolOutputDefinition{
				Schema: json.RawMessage(`{"type":"object"}`),
				Renderer: tools.OutputRendererFunc(func(
					_ json.RawMessage,
					value json.RawMessage,
				) ([]llm.ContentBlock, error) {
					return []llm.ContentBlock{
						llm.NewTextBlock(string(value)),
					}, nil
				}),
			},
			Executor: tools.ExecutorFunc(toolBody),
			ConcurrencyBehavior: tools.ConcurrencyClassifierFunc(func(
				json.RawMessage,
			) bool {
				return true
			}),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func lastTurnEnd(
	t *testing.T,
	conversation session.Context,
) turnEndObservation {
	t.Helper()
	events := conversation.Events()
	for eventIndex := len(events) - 1; eventIndex >= 0; eventIndex-- {
		if events[eventIndex].Type != session.TurnEndEventName {
			continue
		}
		var payload struct {
			Reason turnEndObservation `json:"reason"`
		}
		if err := json.Unmarshal(events[eventIndex].Data, &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Reason
	}
	t.Fatal("turn/end event is absent")
	return turnEndObservation{}
}

func messageTexts(messages []llm.Message) []string {
	texts := make([]string, 0, len(messages))
	for _, message := range messages {
		blocks := message.ContentValue()
		if len(blocks) != 1 {
			texts = append(texts, "multiple-blocks")
			continue
		}
		switch block := blocks[0].(type) {
		case llm.TextBlock:
			texts = append(texts, block.Text)
		case llm.ToolCallBlock:
			texts = append(texts, "tool-call:"+block.Name)
		case llm.ToolResultBlock:
			if len(block.Content) == 1 {
				if textBlock, ok := block.Content[0].(llm.TextBlock); ok {
					texts = append(texts, textBlock.Text)
					continue
				}
			}
			texts = append(texts, "tool-result")
		default:
			texts = append(texts, block.ContentType())
		}
	}
	return texts
}
