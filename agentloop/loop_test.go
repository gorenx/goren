package agentloop_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
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
			plugin.EventOf[session.SessionCreated](),
			plugin.EventOf[agent.Created](),
			plugin.EventOf[agent.SessionStarted](),
			plugin.EventOf[agent.Disposed](),
			plugin.EventOf[session.SessionDisposed](),
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
	case session.SessionCreated:
		entry = session.SessionCreatedEventName
	case agent.Created:
		entry = agent.CreatedEventName
	case agent.SessionStarted:
		entry = agent.SessionStartEventName
	case agent.Disposed:
		entry = agent.DisposedEventName
	case session.SessionDisposed:
		entry = session.SessionDisposedEventName
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
	loopPlugin    *agentloop.LoopPlugin
	backend       *scriptedAdapter
	lifecycle     *lifecycleObserver
}

func newHarnessFixture(
	t *testing.T,
	responses [][]llm.StreamChunk,
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
	loopSettings, err := agentloop.ValidateConfig(agentloop.Config{})
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
	if _, err = runtimeEngine.Start(
		context.Background(),
		lifecycle,
		agentRegistry,
		sessionStore,
		modelRuntime,
		promptRuntime,
		toolService,
		loopPlugin,
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
		session.SessionCreatedEventName,
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
		session.SessionDisposedEventName,
	)
	if got := state.lifecycle.snapshot(); !reflect.DeepEqual(got, wantComplete) {
		t.Fatalf("complete lifecycle = %#v, want %#v", got, wantComplete)
	}
}

type requestExtension struct {
	plugin.Base
	subject  agent.Agent
	disposed bool
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
			Extensions: []plugin.Plugin{
				extension,
			},
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
			Extensions: []plugin.Plugin{
				extension,
			},
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
	handleState, err := state.loopPlugin.Create(
		context.Background(),
		"shutdown-agent",
		agent.Options{
			Provider: "mock",
			Model:    "model",
		},
		session.Metadata{},
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
	if _, err = state.loopPlugin.Create(
		context.Background(),
		"after-shutdown",
		agent.Options{
			Provider: "mock",
			Model:    "model",
		},
		session.Metadata{},
	); err == nil || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("post-shutdown Create error = %v", err)
	}
	want := []string{
		session.SessionCreatedEventName,
		agent.CreatedEventName,
		agent.SessionStartEventName,
		agent.DisposedEventName,
		session.SessionDisposedEventName,
	}
	if got := state.lifecycle.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown lifecycle = %#v, want %#v", got, want)
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
			Extensions: []plugin.Plugin{
				extension,
			},
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
	err := state.toolCatalog.AddTool(
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
		Kind string `json:"kind"`
	} `json:"reason,omitempty"`
}

func lastTurnEnd(
	t *testing.T,
	conversation *session.Session,
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
