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
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/toolaskuser"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/userquestions"
)

type postCommitFailureSink struct{}

type sessionStoreProbe struct {
	plugin.Base
	store session.LiveStore
}

func (*sessionStoreProbe) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "agentloop-session-store-probe",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[session.LiveStore](),
		},
	}
}

func (probe *sessionStoreProbe) Apply(context.Context) error {
	liveStore, err := plugin.Require[session.LiveStore](probe)
	if err != nil {
		return err
	}
	probe.store = liveStore
	return nil
}

func (*sessionStoreProbe) Dispose(context.Context) error { return nil }

type requestResolutionAction struct {
	resolution agent.RequestResolution
}

func (action requestResolutionAction) Execute(
	context.Context,
	agent.RequestNotice,
) (agent.RequestResolution, error) {
	return action.resolution, nil
}

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
	mutex        sync.Mutex
	responses    [][]llm.StreamChunk
	requests     []llm.GenerateOptions
	prefixSource func() []session.Event
	prefixes     [][]session.Event
}

func (backend *scriptedAdapter) Stream(
	_ context.Context,
	requestOptions llm.GenerateOptions,
) (llm.ChunkStream, error) {
	backend.mutex.Lock()
	var prefix []session.Event
	if backend.prefixSource != nil {
		prefix = backend.prefixSource()
	}
	deferred, err := llm.CloneGenerateOptions(requestOptions)
	if err != nil {
		backend.mutex.Unlock()
		return nil, err
	}
	backend.requests = append(backend.requests, deferred)
	backend.prefixes = append(backend.prefixes, prefix)
	requestIndex := len(backend.requests) - 1
	if requestIndex >= len(backend.responses) {
		backend.mutex.Unlock()
		return nil, errors.New("test adapter has no scripted response")
	}
	response := backend.responses[requestIndex]
	backend.mutex.Unlock()
	return llm.NewSliceStream(response)
}

func (backend *scriptedAdapter) capturePrefixes(
	source func() []session.Event,
) {
	backend.mutex.Lock()
	backend.prefixSource = source
	backend.mutex.Unlock()
}

func (backend *scriptedAdapter) prefixSnapshots() [][]session.Event {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	result := make([][]session.Event, len(backend.prefixes))
	for index, prefix := range backend.prefixes {
		result[index] = append([]session.Event(nil), prefix...)
	}
	return result
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
	mutex              sync.Mutex
	entries            []string
	disposedAgentIDs   []session.SessionID
	disposedSessionIDs []session.SessionID
}

type agentCreatedRejector struct {
	plugin.Base
}

func (*agentCreatedRejector) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "agentloop-test-agent-creation-veto",
		Events: []plugin.EventSubscription{
			plugin.EventOf[agent.Created](),
		},
	}
}

func (*agentCreatedRejector) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*agentCreatedRejector) Dispose(context.Context) error {
	return nil
}

func (*agentCreatedRejector) ObserveEvent(
	context.Context,
	plugin.Event,
) error {
	return errors.New("test: Agent Created listener rejected publication")
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
	var disposedAgentID session.SessionID
	var disposedSessionID session.SessionID
	switch lifecycleEvent := fact.(type) {
	case session.Created:
		entry = session.CreatedEventName
	case agent.Created:
		entry = agent.CreatedEventName
	case agent.SessionStarted:
		entry = agent.SessionStartEventName
	case agent.Disposed:
		entry = agent.DisposedEventName
		disposedAgentID = lifecycleEvent.Subject.ID()
	case session.Disposed:
		entry = session.DisposedEventName
		disposedSessionID = lifecycleEvent.Conversation.ID()
	}
	if entry == "" {
		return nil
	}
	observerState.mutex.Lock()
	observerState.entries = append(observerState.entries, entry)
	if disposedAgentID != "" {
		observerState.disposedAgentIDs = append(
			observerState.disposedAgentIDs,
			disposedAgentID,
		)
	}
	if disposedSessionID != "" {
		observerState.disposedSessionIDs = append(
			observerState.disposedSessionIDs,
			disposedSessionID,
		)
	}
	observerState.mutex.Unlock()
	return nil
}

func (observerState *lifecycleObserver) disposedIDs() (
	[]session.SessionID,
	[]session.SessionID,
) {
	observerState.mutex.Lock()
	defer observerState.mutex.Unlock()
	return append([]session.SessionID(nil), observerState.disposedAgentIDs...),
		append([]session.SessionID(nil), observerState.disposedSessionIDs...)
}

func (observerState *lifecycleObserver) snapshot() []string {
	observerState.mutex.Lock()
	defer observerState.mutex.Unlock()
	return append([]string(nil), observerState.entries...)
}

type harnessFixture struct {
	runtimeEngine  *plugin.Runtime
	registryHandle plugin.Handle
	sessionHandle  plugin.Handle
	agents         *agent.RegistryService
	sessions       session.LiveStore
	models         *llm.Runtime
	toolCatalog    tools.ToolCatalog
	backend        *scriptedAdapter
	lifecycle      *lifecycleObserver
}

type questionProviderPlugin struct {
	plugin.Base

	mutex             sync.Mutex
	registration      *userquestions.ProviderHandle
	activationCount   int
	observedQuestions []userquestions.Request
}

func (*questionProviderPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "agentloop-test-question-provider",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[userquestions.UserQuestions](),
		},
	}
}

func (owner *questionProviderPlugin) Apply(context.Context) error {
	questionService, err := plugin.Require[userquestions.UserQuestions](owner)
	if err != nil {
		return err
	}
	registration, err := questionService.RegisterProvider(owner)
	if err != nil {
		return err
	}
	owner.mutex.Lock()
	owner.registration = registration
	owner.activationCount++
	owner.mutex.Unlock()
	return nil
}

func (owner *questionProviderPlugin) Dispose(context.Context) error {
	owner.mutex.Lock()
	registration := owner.registration
	owner.registration = nil
	owner.mutex.Unlock()
	if registration != nil {
		registration.Unregister()
	}
	return nil
}

func (owner *questionProviderPlugin) Ask(
	_ context.Context,
	request userquestions.Request,
) (userquestions.Answer, error) {
	owner.mutex.Lock()
	owner.observedQuestions = append(owner.observedQuestions, request)
	owner.mutex.Unlock()
	return userquestions.Answer{
		Answers: []userquestions.AnswerItem{
			{
				ID:       "confirm",
				Selected: []string{"yes"},
			},
		},
	}, nil
}

func (owner *questionProviderPlugin) snapshot() (
	int,
	[]userquestions.Request,
) {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	return owner.activationCount,
		append([]userquestions.Request(nil), owner.observedQuestions...)
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
	loopPlugin, err := agentloop.NewPlugin(
		loopSettings,
		agentloop.RuntimeOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	sessionPlugin, err := session.NewPlugin(session.MemoryStoreOptions{
		PostCommitFailures: postCommitFailureSink{},
	})
	if err != nil {
		t.Fatal(err)
	}
	agentRegistry := agent.NewRegistry(agent.RegistryOptions{})
	agentPlugin, err := agent.NewRegistryPlugin(agentRegistry)
	if err != nil {
		t.Fatal(err)
	}
	modelRuntime := llm.NewRuntime(nil)
	promptRuntime := systemprompt.New(
		promptSettings,
		systemprompt.RegistryOptions{},
	)
	toolService := tools.New(toolSettings)
	lifecycle := &lifecycleObserver{}
	storeProbe := &sessionStoreProbe{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: eventFailureSink{},
	})
	rootPlugins := []plugin.Plugin{
		lifecycle,
		agentPlugin,
		sessionPlugin,
		storeProbe,
		modelRuntime,
		promptRuntime,
		toolService,
	}
	rootPlugins = append(rootPlugins, rootExtensions...)
	rootPlugins = append(rootPlugins, loopPlugin)
	handles, err := runtimeEngine.Start(
		context.Background(),
		rootPlugins...,
	)
	if err != nil {
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
		runtimeEngine:  runtimeEngine,
		registryHandle: handles[1],
		sessionHandle:  handles[2],
		agents:         agentRegistry,
		sessions:       storeProbe.store,
		models:         modelRuntime,
		toolCatalog:    toolService,
		backend:        backend,
		lifecycle:      lifecycle,
	}
}

func TestAgentLoopReactivatesAfterSessionProviderReplacement(t *testing.T) {
	state := newHarnessFixture(t, nil)
	first, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "before-session-replacement",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := session.NewPlugin(session.MemoryStoreOptions{
		PostCommitFailures: postCommitFailureSink{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = state.runtimeEngine.Replace(
		context.Background(),
		state.sessionHandle,
		replacement,
	); err != nil {
		t.Fatal(err)
	}
	if _, found := state.agents.Get(first.Subject.ID()); found {
		t.Fatal("provider replacement retained the prior activation Agent")
	}
	second, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "after-session-replacement",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = second.Dispose(context.Background()); err != nil {
		t.Fatal(err)
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

func TestAgentExecutesAskUserQuestionWithExactRegisteredSubject(t *testing.T) {
	providerPlugin := &questionProviderPlugin{}
	state := newHarnessFixtureWithSettings(
		t,
		[][]llm.StreamChunk{
			{
				llm.BlockEndChunk{
					Index: 0,
					Block: agentmessage.ToolCallBlock{
						ID:        "question-call",
						Name:      toolaskuser.Name,
						Arguments: `{"questions":[{"id":"confirm","question":"Proceed?","options":[{"label":"yes"},{"label":"no"}]}]}`,
					},
				},
				llm.FinishChunk{
					Reason: llm.ToolCallsFinish{},
				},
			},
			{
				llm.BlockEndChunk{
					Index: 0,
					Block: agentmessage.NewTextBlock("continued"),
				},
				llm.FinishChunk{
					Reason: llm.StopFinish{},
				},
			},
		},
		agentloop.Settings{
			MaxParallelToolCalls: agentloop.DefaultMaxParallelToolCalls,
		},
		userquestions.NewPlugin(),
		toolaskuser.New(),
		providerPlugin,
	)
	handleState := createTestAgent(t, state, "ask-user-agent")
	if err := handleState.Subject.Followup(
		userMessage(t, "Ask me before proceeding"),
	); err != nil {
		t.Fatal(err)
	}
	waitForIdle(t, handleState.Subject)
	requests := state.backend.snapshots()
	if len(requests) != 2 {
		t.Fatalf("model request count = %d, want 2", len(requests))
	}
	foundAskUser := false
	for _, schema := range requests[0].Tools {
		if schema.Name == toolaskuser.Name {
			foundAskUser = true
			break
		}
	}
	if !foundAskUser {
		t.Fatalf("first Agent request tools = %#v", requests[0].Tools)
	}
	applyCount, questionRequests := providerPlugin.snapshot()
	if applyCount != 2 {
		t.Fatalf("question Provider activation count = %d, want 2", applyCount)
	}
	if len(questionRequests) != 1 ||
		questionRequests[0].Subject != handleState.Subject ||
		!state.agents.Contains(questionRequests[0].Subject) {
		t.Fatalf("question Provider requests = %#v", questionRequests)
	}
	toolCalls := 0
	toolResults := 0
	for _, event := range handleState.Subject.SessionValue().Events() {
		switch event.Type {
		case session.ToolCallEventName:
			toolCalls++
		case session.ToolResultEventName:
			toolResults++
		}
	}
	if toolCalls != 1 || toolResults != 1 {
		t.Fatalf(
			"ask_user_question events = (%d calls, %d results)",
			toolCalls,
			toolResults,
		)
	}
	if err := handleState.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAgentCreatedListenerErrorPublishesPairedDisposal(t *testing.T) {
	state := newHarnessFixtureWithSettings(
		t,
		nil,
		agentloop.Settings{
			MaxParallelToolCalls: agentloop.DefaultMaxParallelToolCalls,
		},
		&agentCreatedRejector{},
	)
	_, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "vetoed-agent-publication",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "listener rejected publication") {
		t.Fatalf("Create error = %v", err)
	}
	want := []string{
		session.CreatedEventName,
		agent.CreatedEventName,
		agent.DisposedEventName,
		session.DisposedEventName,
	}
	if got := state.lifecycle.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("rejected publication lifecycle = %#v, want %#v", got, want)
	}
	if _, found := state.agents.Get("vetoed-agent-publication"); found {
		t.Fatal("rejected Agent remained registered")
	}
	if _, found := state.sessions.Get("vetoed-agent-publication"); found {
		t.Fatal("rejected Agent Session remained registered")
	}
}

type requestExtension struct {
	subject  agent.Agent
	disposed bool
}

type prePublicationExtension struct {
	sendErr error
}

func (extension *prePublicationExtension) Apply(
	_ context.Context,
	subject agent.Agent,
	_ agent.ScopeEditor,
) error {
	extension.sendErr = subject.Followup(userMessageValue("too early"))
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
			Setup: extension,
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

func (extension *requestExtension) Apply(
	_ context.Context,
	subject agent.Agent,
	editor agent.ScopeEditor,
) error {
	extension.subject = subject
	if err := editor.UseRequest(extension); err != nil {
		return err
	}
	return editor.Own(extension)
}

func (extension *requestExtension) Close(context.Context) error {
	extension.disposed = true
	extension.subject = nil
	return nil
}

func (*requestExtension) InterceptRequest(
	requestContext context.Context,
	notice agent.RequestNotice,
	downstream agent.RequestAction,
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
			Setup: extension,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if extension.subject != handleState.Subject {
		t.Fatal("extension did not resolve the exact scoped Agent Service")
	}
	if err = handleState.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !extension.disposed || extension.subject != nil {
		t.Fatal("extension lifecycle was not released with its Agent")
	}
}

type toolExecutionExtension struct {
	invocations atomic.Int64
}

func (extension *toolExecutionExtension) Apply(
	_ context.Context,
	_ agent.Agent,
	editor agent.ScopeEditor,
) error {
	return editor.UseToolExecution(extension)
}

func (extension *toolExecutionExtension) InterceptExecute(
	requestContext context.Context,
	request tools.ExecuteRequest,
	downstream tools.ExecuteAction,
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
			Setup: extension,
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
	applied  bool
	disposed bool
}

func (extension *failingAgentExtension) Apply(
	_ context.Context,
	_ agent.Agent,
	editor agent.ScopeEditor,
) error {
	extension.applied = true
	if err := editor.Own(extension); err != nil {
		return err
	}
	return errors.New("extension activation failed")
}

func (extension *failingAgentExtension) Close(context.Context) error {
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
			Setup: extension,
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

func TestRegistryDeactivationRetiresLiveAgentBeforeRootServices(t *testing.T) {
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
	if err = state.runtimeEngine.Unload(
		context.Background(),
		state.registryHandle,
	); err != nil {
		t.Fatal(err)
	}
	if _, found := state.agents.Get("shutdown-agent"); found {
		t.Fatal("Registry shutdown left the Agent registered")
	}
	if _, found := state.sessions.Get("shutdown-agent"); found {
		t.Fatal("Registry shutdown left the Session registered")
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
	); err == nil || !strings.Contains(err.Error(), "not active") {
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

func TestRegistryDeactivationRetiresRuntimeDescendantsChildFirst(t *testing.T) {
	state := newHarnessFixture(t, nil)
	root, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "shutdown-parent",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "shutdown-child",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
			RuntimeParent: root.Subject,
		},
	); err != nil {
		t.Fatal(err)
	}
	if err = state.runtimeEngine.Unload(
		context.Background(),
		state.registryHandle,
	); err != nil {
		t.Fatal(err)
	}
	agentIDs, sessionIDs := state.lifecycle.disposedIDs()
	want := []session.SessionID{
		"shutdown-child",
		"shutdown-parent",
	}
	if !reflect.DeepEqual(agentIDs, want) {
		t.Fatalf("Agent disposal order = %#v, want %#v", agentIDs, want)
	}
	if !reflect.DeepEqual(sessionIDs, want) {
		t.Fatalf("Session disposal order = %#v, want %#v", sessionIDs, want)
	}
	if len(state.agents.List()) != 0 || len(state.sessions.List()) != 0 {
		t.Fatalf(
			"Registry shutdown retained Agents or Sessions: agents=%d sessions=%d",
			len(state.agents.List()),
			len(state.sessions.List()),
		)
	}
}

type shutdownBarrier struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type constructionBarrier struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (barrier *constructionBarrier) Apply(
	context.Context,
	agent.Agent,
	agent.ScopeEditor,
) error {
	barrier.once.Do(func() {
		close(barrier.entered)
	})
	<-barrier.release
	return nil
}

func TestSameIDCollisionDoesNotEnterSecondConstruction(t *testing.T) {
	state := newHarnessFixture(t, nil)
	baselineStatuses := len(state.runtimeEngine.Statuses())
	barrier := &constructionBarrier{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(barrier.release)
		})
	}
	defer release()
	created := make(chan agent.Handle, 1)
	createErrors := make(chan error, 1)
	go func() {
		handleState, createErr := state.agents.Create(
			context.Background(),
			agent.CreateOptions{
				SessionID: "construction-collision",
				AgentOptions: agent.Options{
					Provider: "mock",
					Model:    "model",
				},
				Setup: barrier,
			},
		)
		if createErr != nil {
			createErrors <- createErr
			return
		}
		created <- handleState
	}()
	select {
	case <-barrier.entered:
	case <-time.After(time.Second):
		t.Fatal("first construction did not reach its provisioning boundary")
	}
	_, err := state.agents.Resume(
		context.Background(),
		agent.ResumeOptions{
			SessionID: "construction-collision",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("colliding Resume error = %v", err)
	}
	release()
	var handleState agent.Handle
	select {
	case err = <-createErrors:
		t.Fatal(err)
	case handleState = <-created:
	case <-time.After(time.Second):
		t.Fatal("first construction did not complete")
	}
	if len(state.agents.List()) != 1 || len(state.sessions.List()) != 1 {
		t.Fatalf(
			"collision live state: agents=%d sessions=%d",
			len(state.agents.List()),
			len(state.sessions.List()),
		)
	}
	if err = handleState.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(state.agents.List()) != 0 || len(state.sessions.List()) != 0 {
		t.Fatalf(
			"collision cleanup: agents=%d sessions=%d",
			len(state.agents.List()),
			len(state.sessions.List()),
		)
	}
	if statuses := len(state.runtimeEngine.Statuses()); statuses != baselineStatuses {
		t.Fatalf("collision retained %d Plugin status entries", statuses-baselineStatuses)
	}
}

func (barrier *shutdownBarrier) Apply(
	_ context.Context,
	_ agent.Agent,
	editor agent.ScopeEditor,
) error {
	return editor.Own(barrier)
}

func (barrier *shutdownBarrier) Close(context.Context) error {
	barrier.once.Do(func() {
		close(barrier.entered)
	})
	<-barrier.release
	return nil
}

func TestRegistryDeactivationClosesAdmissionBeforeAgentScopes(t *testing.T) {
	state := newHarnessFixture(t, nil)
	barrier := &shutdownBarrier{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(barrier.release)
		})
	}
	defer release()
	if _, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "shutdown-admission",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
			Setup: barrier,
		},
	); err != nil {
		t.Fatal(err)
	}
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- state.runtimeEngine.Unload(
			context.Background(),
			state.registryHandle,
		)
	}()
	select {
	case <-barrier.entered:
	case <-time.After(time.Second):
		t.Fatal("Agent Scope did not begin shutdown")
	}
	_, err := state.agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "during-shutdown",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("Create during Agent Scope shutdown error = %v", err)
	}
	release()
	select {
	case err = <-shutdownDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Registry shutdown did not complete")
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
					Block: agentmessage.NewTextBlock("done"),
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
	settled       []agentmessage.CallID
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

func (control *parallelToolControl) settledOrder() []agentmessage.CallID {
	control.mutex.Lock()
	defer control.mutex.Unlock()
	return append([]agentmessage.CallID(nil), control.settled...)
}

func TestParallelToolBodiesCommitResultsAndContextInModelOrder(t *testing.T) {
	state := newHarnessFixture(t, [][]llm.StreamChunk{
		toolCallResponse("call-1", "call-2"),
		{
			llm.BlockEndChunk{
				Index: 0,
				Block: agentmessage.NewTextBlock("done"),
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
		[]agentmessage.CallID{"call-2", "call-1"},
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
				Block: agentmessage.NewTextBlock("done"),
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

func TestMaintenanceCancelPreservesRequestedSuccessorTurn(t *testing.T) {
	state := newHarnessFixture(t, [][]llm.StreamChunk{
		{
			llm.BlockEndChunk{
				Index: 0,
				Block: agentmessage.NewTextBlock("after maintenance"),
			},
			llm.FinishChunk{
				Reason: llm.StopFinish{},
			},
		},
	})
	handleState := createTestAgent(t, state, "maintenance-cancel-wake")
	defer func() {
		if err := handleState.Dispose(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	maintenanceStarted := make(chan struct{})
	maintenanceDone := make(chan error, 1)
	go func() {
		maintenanceDone <- handleState.Subject.RunMaintenance(
			context.Background(),
			func(operationContext context.Context) error {
				close(maintenanceStarted)
				<-operationContext.Done()
				return context.Cause(operationContext)
			},
		)
	}()
	select {
	case <-maintenanceStarted:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not start")
	}
	if err := handleState.Subject.Followup(
		userMessage(t, "queued after maintenance"),
	); err != nil {
		t.Fatal(err)
	}
	handleState.Subject.Cancel(
		agent.UserCancel{},
		agent.CancelOptions{
			KeepInbox: true,
		},
	)
	select {
	case maintenanceErr := <-maintenanceDone:
		if maintenanceErr == nil {
			t.Fatal("canceled maintenance returned no cancellation error")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled maintenance did not settle")
	}
	waitForIdle(t, handleState.Subject)
	if requests := state.backend.snapshots(); len(requests) != 1 {
		t.Fatalf(
			"successor model request count = %d, want 1",
			len(requests),
		)
	}
	ending := lastTurnEnd(t, handleState.Subject.SessionValue())
	if ending.Kind != "completed" {
		t.Fatalf("successor Turn ending = %#v, want completed", ending)
	}
	assertAgentLoopBoundariesPaired(t, handleState.Subject.SessionValue())
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
	assertAgentLoopBoundariesPaired(t, handleState.Subject.SessionValue())
}

func TestCancelDrainsToolAndFlushBeforeIdle(t *testing.T) {
	barrier := &flushBarrier{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	state := newHarnessFixtureWithSettings(
		t,
		[][]llm.StreamChunk{
			toolCallResponse("call-cancel-flush"),
		},
		agentloop.Settings{
			MaxParallelToolCalls: agentloop.DefaultMaxParallelToolCalls,
		},
		barrier,
	)
	toolStarted := make(chan struct{})
	toolSettled := make(chan struct{})
	registerParallelTool(t, state, func(
		_ json.RawMessage,
		runContext tools.ToolRunContext,
	) (json.RawMessage, error) {
		close(toolStarted)
		<-runContext.Context.Done()
		close(toolSettled)
		return json.RawMessage(`{"canceled":true}`), nil
	})
	handleState := createTestAgent(t, state, "cancel-tool-flush")
	released := false
	defer func() {
		if !released {
			close(barrier.release)
		}
		if disposeErr := handleState.Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	}()
	if err := handleState.Subject.Followup(userMessage(t, "cancel")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-toolStarted:
	case <-time.After(time.Second):
		t.Fatal("Tool body did not start")
	}
	handleState.Subject.Cancel(
		agent.UserCancel{},
		agent.CancelOptions{
			KeepInbox: true,
		},
	)
	select {
	case <-toolSettled:
	case <-time.After(time.Second):
		t.Fatal("canceled Tool body did not settle")
	}
	select {
	case <-barrier.started:
	case <-time.After(time.Second):
		t.Fatal("canceled Turn did not reach Flush")
	}
	idle := make(chan error, 1)
	go func() {
		idle <- handleState.Subject.WhenIdle(context.Background())
	}()
	select {
	case idleErr := <-idle:
		t.Fatalf("Agent became idle before canceled Flush completed: %v", idleErr)
	case <-time.After(20 * time.Millisecond):
	}
	assertAgentLoopBoundariesPaired(t, handleState.Subject.SessionValue())
	close(barrier.release)
	released = true
	select {
	case idleErr := <-idle:
		if idleErr != nil {
			t.Fatal(idleErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Agent did not converge after canceled Flush completed")
	}
	ending := lastTurnEnd(t, handleState.Subject.SessionValue())
	if ending.Kind != "aborted" || ending.Reason == nil ||
		ending.Reason.Kind != "user" {
		t.Fatalf("turn ending = %#v, want user cancellation", ending)
	}
	assertSessionEventNames(
		t,
		handleState.Subject.SessionValue(),
		[]string{
			"agent/inbox/spliced",
			"turn/start",
			"agent/inbox/spliced",
			"step/start",
			"user/message",
			"request/header",
			"request/context",
			"assistant/chunk",
			"assistant/chunk",
			"assistant/message",
			"tool/call",
			"tool/result",
			"step/end",
			"turn/end",
		},
	)
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
	assertAgentLoopBoundariesPaired(t, handleState.Subject.SessionValue())
}

type retryExtension struct {
	notices chan agent.RequestErrorNotice
}

func (extension *retryExtension) Apply(
	_ context.Context,
	_ agent.Agent,
	editor agent.ScopeEditor,
) error {
	return editor.UseRequestError(extension)
}

func (extension *retryExtension) InterceptRequestError(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	downstream agent.RequestErrorHandler,
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
				Block: agentmessage.NewTextBlock("recovered"),
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
			Setup: extension,
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
	stepStarts := 0
	stepEnds := 0
	for _, event := range handleState.Subject.SessionValue().Events() {
		switch event.Type {
		case session.StepStartEventName:
			stepStarts++
		case session.StepEndEventName:
			stepEnds++
		}
	}
	if stepStarts != 1 || stepEnds != 1 {
		t.Fatalf(
			"Step lifecycle = (%d starts, %d ends), want one paired Step",
			stepStarts,
			stepEnds,
		)
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
				) ([]agentmessage.ContentBlock, error) {
					return []agentmessage.ContentBlock{
						agentmessage.NewTextBlock(string(value)),
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
				Block: agentmessage.ToolCallBlock{
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
				Block: agentmessage.NewTextBlock("done"),
			},
			llm.FinishChunk{
				Reason: llm.StopFinish{},
			},
		},
	}
}

func userMessage(t *testing.T, content string) agentmessage.UserMessage {
	t.Helper()
	message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock(content),
		},
		Source: agentmessage.UserMessageSource{},
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

func userMessageValue(content string) agentmessage.UserMessage {
	message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock(content),
		},
		Source: agentmessage.UserMessageSource{},
	})
	if err != nil {
		panic(err)
	}
	return message
}

func toolCallResponse(callIDs ...agentmessage.CallID) []llm.StreamChunk {
	response := make([]llm.StreamChunk, 0, len(callIDs)+1)
	for index, callID := range callIDs {
		response = append(
			response,
			llm.BlockEndChunk{
				Index: index,
				Block: agentmessage.ToolCallBlock{
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
				) ([]agentmessage.ContentBlock, error) {
					return []agentmessage.ContentBlock{
						agentmessage.NewTextBlock(string(value)),
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

func assertAgentLoopBoundariesPaired(
	testingState *testing.T,
	conversation session.Context,
) {
	testingState.Helper()
	turnStarts := 0
	turnEnds := 0
	stepStarts := 0
	stepEnds := 0
	toolCalls := make(map[int64]struct{})
	toolResults := make(map[int64]int)
	for _, committed := range conversation.Events() {
		switch committed.Type {
		case session.TurnStartEventName:
			turnStarts++
		case session.TurnEndEventName:
			turnEnds++
		case session.StepStartEventName:
			stepStarts++
		case session.StepEndEventName:
			stepEnds++
		case session.ToolCallEventName:
			toolCalls[committed.Seq] = struct{}{}
		case session.ToolResultEventName:
			if committed.SourceEventSeqs == nil ||
				len(*committed.SourceEventSeqs) != 1 {
				testingState.Fatalf(
					"Tool result seq %d has invalid provenance %#v",
					committed.Seq,
					committed.SourceEventSeqs,
				)
			}
			toolResults[(*committed.SourceEventSeqs)[0]]++
		}
	}
	if turnStarts != turnEnds || stepStarts != stepEnds {
		testingState.Fatalf(
			"unpaired boundaries: Turn %d/%d, Step %d/%d",
			turnStarts,
			turnEnds,
			stepStarts,
			stepEnds,
		)
	}
	for callSequence := range toolCalls {
		if toolResults[callSequence] != 1 {
			testingState.Fatalf(
				"Tool call seq %d has %d results",
				callSequence,
				toolResults[callSequence],
			)
		}
	}
}

func messageTexts(messages []agentmessage.Message) []string {
	texts := make([]string, 0, len(messages))
	for _, message := range messages {
		blocks := message.ContentValue()
		if len(blocks) != 1 {
			texts = append(texts, "multiple-blocks")
			continue
		}
		switch block := blocks[0].(type) {
		case agentmessage.TextBlock:
			texts = append(texts, block.Text)
		case agentmessage.ToolCallBlock:
			texts = append(texts, "tool-call:"+block.Name)
		case agentmessage.ToolResultBlock:
			if len(block.Content) == 1 {
				if textBlock, ok := block.Content[0].(agentmessage.TextBlock); ok {
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
