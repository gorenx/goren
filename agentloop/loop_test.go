package agentloop_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sesssqlite "github.com/gorenx/goren/session/persistence/sqlite"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

type harnessFixture struct {
	engine        *plugin.Runtime
	pluginScope   *plugin.Scope
	agents        agent.Registry
	sessions      session.LiveStore
	models        llm.LlmRuntime
	toolRuntime   tools.ToolRuntime
	prompts       systemprompt.SystemPrompt
	loop          agentloop.Loop
	modelAdapter  *scriptedAdapter
	decorateTools func(tools.ToolRuntime) tools.ToolRuntime
	parallelLimit int
}

type harnessProvider struct {
	state *harnessFixture
}

func (*harnessProvider) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "agent-loop-fixture",
		Provides: []plugin.ServiceRef{
			agent.Service.Ref(), session.StoreService.Ref(), llm.Service.Ref(),
			systemprompt.Service.Ref(), tools.Service.Ref(), agentloop.Service.Ref(),
		},
	}
}

func (provider *harnessProvider) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	agentRegistry, err := agent.NewRegistry(pluginScope, agent.RegistryOptions{})
	if err != nil {
		return err
	}
	sessionStore, err := session.NewMemoryStore(pluginScope, session.MemoryStoreOptions{})
	if err != nil {
		return err
	}
	modelRuntime, err := llm.NewRuntime(pluginScope, nil)
	if err != nil {
		return err
	}
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		return err
	}
	promptRuntime, err := systemprompt.New(requestContext, pluginScope, promptSettings)
	if err != nil {
		return err
	}
	toolSettings, err := tools.ValidateConfig(tools.Config{})
	if err != nil {
		return err
	}
	toolRuntime, err := tools.New(requestContext, pluginScope, promptRuntime, nil, nil, toolSettings)
	if err != nil {
		return err
	}
	loopConfig := agentloop.Config{}
	if provider.state.parallelLimit > 0 {
		loopConfig.MaxParallelToolCalls = &provider.state.parallelLimit
	}
	loopSettings, err := agentloop.ValidateConfig(loopConfig)
	if err != nil {
		return err
	}
	loopTools := tools.ToolRuntime(toolRuntime)
	if provider.state.decorateTools != nil {
		loopTools = provider.state.decorateTools(toolRuntime)
	}
	loopRuntime, err := agentloop.New(requestContext, pluginScope, agentloop.Dependencies{
		Agents: agentRegistry, Sessions: sessionStore, LLM: modelRuntime,
		Tools: loopTools, SystemPrompt: promptRuntime,
	}, loopSettings, agentloop.RuntimeOptions{})
	if err != nil {
		return err
	}
	services := []func() error{
		func() error {
			_, provideErr := plugin.Provide(pluginScope, agent.Service, agentRegistry)
			return provideErr
		},
		func() error {
			_, provideErr := plugin.Provide(pluginScope, session.StoreService, session.LiveStore(sessionStore))
			return provideErr
		},
		func() error {
			_, provideErr := plugin.Provide(pluginScope, llm.Service, modelRuntime)
			return provideErr
		},
		func() error {
			_, provideErr := plugin.Provide(pluginScope, systemprompt.Service, promptRuntime)
			return provideErr
		},
		func() error {
			_, provideErr := plugin.Provide(pluginScope, tools.Service, toolRuntime)
			return provideErr
		},
		func() error {
			_, provideErr := plugin.Provide(pluginScope, agentloop.Service, loopRuntime)
			return provideErr
		},
	}
	for _, provideService := range services {
		if err := provideService(); err != nil {
			return err
		}
	}
	provider.state.pluginScope = pluginScope
	provider.state.agents = agentRegistry
	provider.state.sessions = sessionStore
	provider.state.models = modelRuntime
	provider.state.toolRuntime = toolRuntime
	provider.state.prompts = promptRuntime
	provider.state.loop = loopRuntime
	return nil
}

type scriptedAdapter struct {
	mu        sync.Mutex
	responses [][]llm.StreamChunk
	requests  []llm.GenerateOptions
	before    func(int)
}

func (backend *scriptedAdapter) Stream(_ context.Context, options llm.GenerateOptions) (llm.ChunkStream, error) {
	backend.mu.Lock()
	deferred, err := llm.CloneGenerateOptions(options)
	if err != nil {
		backend.mu.Unlock()
		return nil, err
	}
	backend.requests = append(backend.requests, deferred)
	index := len(backend.requests) - 1
	if index >= len(backend.responses) {
		backend.mu.Unlock()
		return nil, errors.New("test adapter has no scripted response")
	}
	response := backend.responses[index]
	before := backend.before
	backend.mu.Unlock()
	if before != nil {
		before(index)
	}
	return llm.NewSliceStream(response)
}

func (backend *scriptedAdapter) snapshots() []llm.GenerateOptions {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	result := make([]llm.GenerateOptions, 0, len(backend.requests))
	for _, request := range backend.requests {
		detached, _ := llm.CloneGenerateOptions(request)
		result = append(result, detached)
	}
	return result
}

func newHarnessFixture(t *testing.T, responses [][]llm.StreamChunk) *harnessFixture {
	t.Helper()
	return newHarnessFixtureWithTools(t, responses, 0, nil)
}

func newHarnessFixtureWithTools(
	t *testing.T,
	responses [][]llm.StreamChunk,
	parallelLimit int,
	decorateTools func(tools.ToolRuntime) tools.ToolRuntime,
) *harnessFixture {
	t.Helper()
	state := &harnessFixture{
		engine: plugin.NewRuntime(), modelAdapter: &scriptedAdapter{responses: responses},
		parallelLimit: parallelLimit, decorateTools: decorateTools,
	}
	if _, err := state.engine.Load(context.Background(), &harnessProvider{state: state}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.models.RegisterAdapter(context.Background(), state.pluginScope, []string{"mock"}, state.modelAdapter); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.engine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return state
}

type injectedSchedulerRuntime struct {
	tools.ToolRuntime
	staged tools.ToolExecutionScheduler
}

func (carrier *injectedSchedulerRuntime) Scheduler() tools.ToolExecutionScheduler {
	return carrier.staged
}

type dispatchFailureScheduler struct {
	delegate        tools.ToolExecutionScheduler
	failureCallID   llm.CallID
	waitForStarted  <-chan struct{}
	failureReturned chan<- struct{}
}

func (bridge *dispatchFailureScheduler) Prepare(
	requestContext context.Context,
	input tools.ToolExecutionInput,
) (tools.ScheduledToolPreparation, error) {
	return bridge.delegate.Prepare(requestContext, input)
}

func (bridge *dispatchFailureScheduler) Dispatch(
	execution tools.ToolExecution,
) (tools.ScheduledToolDispatch, error) {
	if execution.CallID != bridge.failureCallID {
		return bridge.delegate.Dispatch(execution)
	}
	<-bridge.waitForStarted
	close(bridge.failureReturned)
	return tools.ScheduledToolDispatch{}, errors.New("scheduler failed")
}

func (bridge *dispatchFailureScheduler) Finalize(
	execution tools.ToolExecution,
	outcome tools.ToolExecutionResult,
) (tools.ToolExecutionResult, error) {
	return bridge.delegate.Finalize(execution, outcome)
}

func (bridge *dispatchFailureScheduler) Finish(
	execution tools.ToolExecution,
	outcome tools.ToolExecutionResult,
) tools.ToolExecutionResult {
	return bridge.delegate.Finish(execution, outcome)
}

func userMessage(t *testing.T, text string, source llm.MessageSource) llm.UserMessage {
	t.Helper()
	message, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock(text)}, Source: source,
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func modelResponses() [][]llm.StreamChunk {
	return [][]llm.StreamChunk{
		{
			llm.BlockEndChunk{Index: 0, Block: llm.ToolCallBlock{
				ID: "call-1", Name: "echo", Arguments: `{"value":"hello"}`,
			}},
			llm.FinishChunk{Reason: llm.ToolCallsFinish{}},
		},
		{
			llm.BlockEndChunk{Index: 0, Block: llm.NewTextBlock("done")},
			llm.FinishChunk{Reason: llm.StopFinish{}},
		},
	}
}

func registerEchoTool(t *testing.T, state *harnessFixture) {
	t.Helper()
	deferredContext := userMessage(t, "tool-context", llm.PluginMessageSource{Plugin: "echo-tool"})
	_, err := state.toolRuntime.Register(context.Background(), state.pluginScope, tools.ToolDefinition{
		Name: "echo", Description: "echo one object",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Output: tools.ToolOutputDefinition{
			Schema: json.RawMessage(`{"type":"object"}`),
			Renderer: tools.OutputRendererFunc(func(_ json.RawMessage, value json.RawMessage) ([]llm.ContentBlock, error) {
				return []llm.ContentBlock{llm.NewTextBlock(string(value))}, nil
			}),
		},
		Executor: tools.ExecutorFunc(func(arguments json.RawMessage, runContext tools.ToolRunContext) (json.RawMessage, error) {
			runContext.DeferContext(deferredContext)
			return append(json.RawMessage(nil), arguments...), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTurnEndAwaitsSessionDurabilityBeforeAgentBecomesIdle(t *testing.T) {
	state := newHarnessFixture(t, [][]llm.StreamChunk{{
		llm.BlockEndChunk{Index: 0, Block: llm.NewTextBlock("done")},
		llm.FinishChunk{Reason: llm.StopFinish{}},
	}})
	requestContext := context.Background()
	sqliteBackend, err := sesssqlite.Open(requestContext, sesssqlite.Config{
		Path: t.TempDir() + "/sessions.sqlite", JournalMode: sesssqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sesspersist.NewSessionLogStore(
		requestContext,
		state.pluginScope,
		state.sessions,
		sqliteBackend,
		sesspersist.SessionLogStoreOptions{WriteBatchMaxDelay: time.Hour},
	); err != nil {
		_ = sqliteBackend.Close(requestContext)
		t.Fatal(err)
	}
	flushStarted := make(chan struct{})
	releaseFlush := make(chan struct{})
	var signalOnce, releaseOnce sync.Once
	releaseCheckpoint := func() { releaseOnce.Do(func() { close(releaseFlush) }) }
	defer releaseCheckpoint()
	if _, err := session.OnFlush(state.pluginScope, func(_ context.Context, conversation *session.Session) error {
		events := conversation.Events()
		if len(events) == 0 || events[len(events)-1].Type != session.TurnEndEventName {
			return errors.New("durability checkpoint ran before turn/end committed")
		}
		signalOnce.Do(func() { close(flushStarted) })
		<-releaseFlush
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	handle, err := state.agents.Create(requestContext, state.pluginScope, agent.CreateOptions{
		SessionID: "turn-checkpoint", AgentOptions: agent.Options{Provider: "mock", Model: "model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if disposeErr := handle.Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	}()
	if err := handle.Subject.Followup(userMessage(t, "run", llm.UserMessageSource{})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-flushStarted:
	case <-time.After(time.Second):
		t.Fatal("turn did not reach the durability checkpoint")
	}
	var durableEvents []session.Event
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		storedPrefix, found, loadErr := sqliteBackend.LoadStored(requestContext, "turn-checkpoint")
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if found && len(storedPrefix.Events) != 0 && storedPrefix.Events[len(storedPrefix.Events)-1].Type == session.TurnEndEventName {
			durableEvents = storedPrefix.Events
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(durableEvents) == 0 {
		t.Fatal("turn/end was not durable while the checkpoint was in progress")
	}
	if status := handle.Subject.StatusValue(); status != agent.StatusRunning {
		t.Fatalf("status while flush is pending = %q, want running", status)
	}
	idle := make(chan error, 1)
	go func() { idle <- handle.Subject.WhenIdle(context.Background()) }()
	select {
	case err := <-idle:
		t.Fatalf("WhenIdle returned before durability checkpoint: %v", err)
	default:
	}
	releaseCheckpoint()
	select {
	case err := <-idle:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Agent did not become idle after durability checkpoint")
	}
}

func TestParallelBodiesCommitResultsAndContextsInModelOrder(t *testing.T) {
	responses := [][]llm.StreamChunk{
		{
			llm.BlockEndChunk{Index: 0, Block: llm.ToolCallBlock{
				ID: "call-1", Name: "parallel", Arguments: `{"id":1}`,
			}},
			llm.BlockEndChunk{Index: 1, Block: llm.ToolCallBlock{
				ID: "call-2", Name: "parallel", Arguments: `{"id":2}`,
			}},
			llm.FinishChunk{Reason: llm.ToolCallsFinish{}},
		},
		{
			llm.BlockEndChunk{Index: 0, Block: llm.NewTextBlock("done")},
			llm.FinishChunk{Reason: llm.StopFinish{}},
		},
	}
	state := newHarnessFixture(t, responses)
	started := make(chan int, 2)
	releaseFirst := make(chan struct{})
	contexts := []llm.UserMessage{
		userMessage(t, "context-1", llm.PluginMessageSource{Plugin: "parallel-tool"}),
		userMessage(t, "context-2", llm.PluginMessageSource{Plugin: "parallel-tool"}),
	}
	_, err := state.toolRuntime.Register(context.Background(), state.pluginScope, tools.ToolDefinition{
		Name: "parallel", Parameters: json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"integer"}}}`),
		Output: tools.ToolOutputDefinition{
			Schema: json.RawMessage(`{"type":"object"}`),
			Renderer: tools.OutputRendererFunc(func(_ json.RawMessage, value json.RawMessage) ([]llm.ContentBlock, error) {
				return []llm.ContentBlock{llm.NewTextBlock(string(value))}, nil
			}),
		},
		ConcurrencyBehavior: tools.ConcurrencyClassifierFunc(func(json.RawMessage) bool { return true }),
		Executor: tools.ExecutorFunc(func(arguments json.RawMessage, runContext tools.ToolRunContext) (json.RawMessage, error) {
			var input struct {
				ID int `json:"id"`
			}
			if decodeErr := json.Unmarshal(arguments, &input); decodeErr != nil {
				return nil, decodeErr
			}
			started <- input.ID
			if input.ID == 1 {
				<-releaseFirst
			}
			runContext.DeferContext(contexts[input.ID-1])
			return append(json.RawMessage(nil), arguments...), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := state.agents.Create(context.Background(), state.pluginScope, agent.CreateOptions{
		SessionID: "parallel-order", AgentOptions: agent.Options{Provider: "mock", Model: "model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if disposeErr := handle.Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	}()
	if err := handle.Subject.Followup(userMessage(t, "run", llm.UserMessageSource{})); err != nil {
		t.Fatal(err)
	}
	seenStarts := map[int]bool{}
	for len(seenStarts) < 2 {
		select {
		case identifier := <-started:
			seenStarts[identifier] = true
		case <-time.After(time.Second):
			t.Fatalf("parallel starts = %#v", seenStarts)
		}
	}
	close(releaseFirst)
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := handle.Subject.WhenIdle(waitContext); err != nil {
		t.Fatal(err)
	}

	orderedEvents := make([]string, 0, 4)
	for _, event := range handle.Subject.SessionValue().Events() {
		switch event.Type {
		case session.ToolCallEventName:
			var payload session.ToolCall
			if err := json.Unmarshal(event.Data, &payload); err != nil {
				t.Fatal(err)
			}
			orderedEvents = append(orderedEvents, "call:"+string(payload.CallID))
		case session.ToolResultEventName:
			var payload struct {
				Message struct {
					Content []struct {
						ToolCallID llm.CallID `json:"toolCallId"`
					} `json:"content"`
				} `json:"message"`
			}
			if err := json.Unmarshal(event.Data, &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Message.Content) != 1 {
				t.Fatalf("tool result content = %#v", payload.Message.Content)
			}
			orderedEvents = append(orderedEvents, "result:"+string(payload.Message.Content[0].ToolCallID))
		}
	}
	if want := []string{"call:call-1", "call:call-2", "result:call-1", "result:call-2"}; !reflect.DeepEqual(orderedEvents, want) {
		t.Fatalf("tool event order = %#v, want %#v", orderedEvents, want)
	}
	requests := state.modelAdapter.snapshots()
	if len(requests) != 2 || len(requests[1].Messages) != 6 {
		t.Fatalf("continuation request = %#v", requests)
	}
	if firstResult := toolResultCallID(requests[1].Messages[2]); firstResult != "call-1" {
		t.Fatalf("first derived result = %q", firstResult)
	}
	if secondResult := toolResultCallID(requests[1].Messages[3]); secondResult != "call-2" {
		t.Fatalf("second derived result = %q", secondResult)
	}
	if got := messageTexts(requests[1].Messages[4:]); !reflect.DeepEqual(got, []string{"context-1", "context-2"}) {
		t.Fatalf("derived context order = %#v", got)
	}
}

func TestSchedulerFailureStopsReplenishmentAndDrainsStartedDispatch(t *testing.T) {
	responses := [][]llm.StreamChunk{{
		llm.BlockEndChunk{Index: 0, Block: llm.ToolCallBlock{ID: "call-1", Name: "parallel", Arguments: `{"id":1}`}},
		llm.BlockEndChunk{Index: 1, Block: llm.ToolCallBlock{ID: "call-2", Name: "parallel", Arguments: `{"id":2}`}},
		llm.BlockEndChunk{Index: 2, Block: llm.ToolCallBlock{ID: "call-3", Name: "parallel", Arguments: `{"id":3}`}},
		llm.FinishChunk{Reason: llm.ToolCallsFinish{}},
	}}
	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	failureReturned := make(chan struct{})
	state := newHarnessFixtureWithTools(t, responses, 2, func(baseRuntime tools.ToolRuntime) tools.ToolRuntime {
		return &injectedSchedulerRuntime{
			ToolRuntime: baseRuntime,
			staged: &dispatchFailureScheduler{
				delegate: baseRuntime.Scheduler(), failureCallID: "call-1",
				waitForStarted: secondStarted, failureReturned: failureReturned,
			},
		}
	})
	var startedMu sync.Mutex
	startedIDs := make([]int, 0, 3)
	_, err := state.toolRuntime.Register(context.Background(), state.pluginScope, tools.ToolDefinition{
		Name: "parallel", Parameters: json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"integer"}}}`),
		Output: tools.ToolOutputDefinition{
			Schema: json.RawMessage(`{"type":"object"}`),
			Renderer: tools.OutputRendererFunc(func(_ json.RawMessage, value json.RawMessage) ([]llm.ContentBlock, error) {
				return []llm.ContentBlock{llm.NewTextBlock(string(value))}, nil
			}),
		},
		ConcurrencyBehavior: tools.ConcurrencyClassifierFunc(func(json.RawMessage) bool { return true }),
		Executor: tools.ExecutorFunc(func(arguments json.RawMessage, _ tools.ToolRunContext) (json.RawMessage, error) {
			var input struct {
				ID int `json:"id"`
			}
			if decodeErr := json.Unmarshal(arguments, &input); decodeErr != nil {
				return nil, decodeErr
			}
			startedMu.Lock()
			startedIDs = append(startedIDs, input.ID)
			startedMu.Unlock()
			if input.ID == 2 {
				close(secondStarted)
				<-releaseSecond
			}
			return append(json.RawMessage(nil), arguments...), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := state.loop.Create(
		context.Background(), state.pluginScope, "scheduler-failure",
		agent.Options{Provider: "mock", Model: "model"}, session.Metadata{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if disposeErr := handle.Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	}()
	if err := handle.Subject.Followup(userMessage(t, "run", llm.UserMessageSource{})); err != nil {
		t.Fatal(err)
	}
	idleDone := make(chan error, 1)
	go func() {
		idleDone <- handle.Subject.WhenIdle(context.Background())
	}()
	select {
	case <-failureReturned:
	case <-time.After(time.Second):
		t.Fatal("scheduler failure did not wait for the sibling dispatch to start")
	}
	select {
	case idleErr := <-idleDone:
		t.Fatalf("Agent became idle before the started sibling drained: %v", idleErr)
	default:
	}
	close(releaseSecond)
	select {
	case idleErr := <-idleDone:
		if idleErr != nil {
			t.Fatal(idleErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Agent did not settle after the started sibling drained")
	}
	startedMu.Lock()
	actualStarted := append([]int(nil), startedIDs...)
	startedMu.Unlock()
	if want := []int{2}; !reflect.DeepEqual(actualStarted, want) {
		t.Fatalf("started Tool bodies = %#v, want %#v", actualStarted, want)
	}
	callIDs := make([]llm.CallID, 0, 2)
	resultCount := 0
	for _, event := range handle.Subject.SessionValue().Events() {
		switch event.Type {
		case session.ToolCallEventName:
			var payload session.ToolCall
			if err := json.Unmarshal(event.Data, &payload); err != nil {
				t.Fatal(err)
			}
			callIDs = append(callIDs, payload.CallID)
		case session.ToolResultEventName:
			resultCount++
		}
	}
	if want := []llm.CallID{"call-1", "call-2"}; !reflect.DeepEqual(callIDs, want) {
		t.Fatalf("recorded Tool calls = %#v, want %#v", callIDs, want)
	}
	if resultCount != 0 {
		t.Fatalf("Tool result count = %d, want 0 after scheduler failure", resultCount)
	}
	if ending := lastTurnEnd(t, handle.Subject.SessionValue()); ending.Kind != "error" {
		t.Fatalf("turn ending = %#v, want error", ending)
	}
}

func TestLoopPublishesDrivesAndDisposesOneAgentLifecycle(t *testing.T) {
	state := newHarnessFixture(t, modelResponses())
	registerEchoTool(t, state)

	var lifecycleMu sync.Mutex
	lifecycle := make([]string, 0, 5)
	record := func(entry string) {
		lifecycleMu.Lock()
		lifecycle = append(lifecycle, entry)
		lifecycleMu.Unlock()
	}
	if _, err := session.OnCreated(state.pluginScope, func(context.Context, *session.Session) error {
		record("session/created")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.OnCreated(state.pluginScope, func(context.Context, agent.Agent) error {
		record("agent/created")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.OnSessionStart(state.pluginScope, func(context.Context, agent.Agent, agent.SessionStartSource) error {
		record("agent/session-start")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.OnDisposed(state.pluginScope, func(context.Context, agent.Agent) error {
		record("agent/disposed")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.OnDisposed(state.pluginScope, func(context.Context, *session.Session) error {
		record("session/disposed")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	handle, err := state.agents.Create(context.Background(), state.pluginScope, agent.CreateOptions{
		SessionID: "loop-e2e", AgentOptions: agent.Options{Provider: "mock", Model: "model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if handle.Subject.ID() != "loop-e2e" || handle.Subject.SessionValue().ID() != "loop-e2e" {
		t.Fatalf("Agent/Session identity diverged: %q / %q", handle.Subject.ID(), handle.Subject.SessionValue().ID())
	}
	if err := handle.Subject.Followup(userMessage(t, "hello", llm.UserMessageSource{})); err != nil {
		t.Fatal(err)
	}
	if err := handle.Subject.WhenIdle(context.Background()); err != nil {
		t.Fatal(err)
	}

	requests := state.modelAdapter.snapshots()
	if len(requests) != 2 {
		t.Fatalf("model request count = %d, want 2", len(requests))
	}
	if got := messageTexts(requests[0].Messages); !reflect.DeepEqual(got, []string{"hello"}) {
		t.Fatalf("first request messages = %#v", got)
	}
	if len(requests[0].Tools) != 1 || requests[0].Tools[0].Name != "echo" {
		t.Fatalf("first request tools = %#v", requests[0].Tools)
	}
	if got := messageTexts(requests[1].Messages); !reflect.DeepEqual(got, []string{
		"hello", "tool-call:echo", `{"value":"hello"}`, "tool-context",
	}) {
		t.Fatalf("second request messages = %#v", got)
	}

	eventTypes := make([]string, 0)
	for _, event := range handle.Subject.SessionValue().Events() {
		eventTypes = append(eventTypes, event.Type)
	}
	wantEvents := []string{
		"agent/inbox/spliced", "turn/start", "agent/inbox/spliced", "step/start", "user/message",
		"request/header", "request/context", "assistant/chunk", "assistant/chunk", "assistant/message",
		"tool/call", "tool/result", "agent/inbox/spliced", "step/end", "agent/inbox/spliced",
		"step/start", "user/message", "assistant/chunk", "assistant/chunk", "assistant/message", "step/end", "turn/end",
	}
	if !reflect.DeepEqual(eventTypes, wantEvents) {
		t.Fatalf("Session events = %#v, want %#v", eventTypes, wantEvents)
	}
	if got := lifecycleSnapshot(&lifecycleMu, lifecycle); !reflect.DeepEqual(got, []string{
		"session/created", "agent/created", "agent/session-start",
	}) {
		t.Fatalf("publication lifecycle = %#v", got)
	}

	if err := handle.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, found := state.agents.Get("loop-e2e"); found {
		t.Fatal("disposed Agent remains registered")
	}
	if _, found := state.sessions.Get("loop-e2e"); found {
		t.Fatal("disposed Session remains registered")
	}
	if got := lifecycleSnapshot(&lifecycleMu, lifecycle); !reflect.DeepEqual(got, []string{
		"session/created", "agent/created", "agent/session-start", "agent/disposed", "session/disposed",
	}) {
		t.Fatalf("complete lifecycle = %#v", got)
	}
}

func TestMaintenanceLatchedWakeKeepsWhenIdleBehindSuccessorTurn(t *testing.T) {
	state := newHarnessFixture(t, [][]llm.StreamChunk{{
		llm.BlockEndChunk{Index: 0, Block: llm.NewTextBlock("done")},
		llm.FinishChunk{Reason: llm.StopFinish{}},
	}})
	modelStarted := make(chan struct{})
	releaseModel := make(chan struct{})
	state.modelAdapter.before = func(int) {
		close(modelStarted)
		<-releaseModel
	}
	handle, err := state.loop.Create(
		context.Background(), state.pluginScope, "maintenance-handoff",
		agent.Options{Provider: "mock", Model: "model"}, session.Metadata{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if disposeErr := handle.Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	}()

	maintenanceStarted := make(chan struct{})
	releaseMaintenance := make(chan struct{})
	maintenanceDone := make(chan error, 1)
	go func() {
		maintenanceDone <- handle.Subject.RunMaintenance(context.Background(), agent.MaintenanceFunc(
			func(context.Context) error {
				close(maintenanceStarted)
				<-releaseMaintenance
				return nil
			},
		))
	}()
	<-maintenanceStarted
	if err := handle.Subject.Followup(userMessage(t, "queued", llm.UserMessageSource{})); err != nil {
		t.Fatal(err)
	}
	idleDone := make(chan error, 1)
	go func() {
		idleDone <- handle.Subject.WhenIdle(context.Background())
	}()
	close(releaseMaintenance)
	if err := <-maintenanceDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-modelStarted:
	case <-time.After(time.Second):
		t.Fatal("latched wake did not start its successor turn")
	}
	select {
	case err := <-idleDone:
		t.Fatalf("WhenIdle returned before successor turn settled: %v", err)
	default:
	}
	close(releaseModel)
	select {
	case err := <-idleDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("WhenIdle did not follow the latched successor turn")
	}
	if requests := state.modelAdapter.snapshots(); len(requests) != 1 {
		t.Fatalf("model request count = %d, want 1", len(requests))
	}
}

func TestFirstCancelCauseSurvivesDisposalRace(t *testing.T) {
	state := newHarnessFixture(t, [][]llm.StreamChunk{{
		llm.BlockEndChunk{Index: 0, Block: llm.ToolCallBlock{
			ID: "call-1", Name: "blocking", Arguments: `{}`,
		}},
		llm.FinishChunk{Reason: llm.ToolCallsFinish{}},
	}})
	durabilityErrors := make(chan error, 1)
	if _, err := session.OnFlush(state.pluginScope, func(checkpointContext context.Context, _ *session.Session) error {
		durabilityErrors <- checkpointContext.Err()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	bodyStarted := make(chan struct{})
	bodyCanceled := make(chan struct{})
	releaseBody := make(chan struct{})
	_, err := state.toolRuntime.Register(context.Background(), state.pluginScope, tools.ToolDefinition{
		Name: "blocking", Parameters: json.RawMessage(`{"type":"object"}`),
		Output: tools.ToolOutputDefinition{
			Schema: json.RawMessage(`{"type":"object"}`),
			Renderer: tools.OutputRendererFunc(func(_ json.RawMessage, value json.RawMessage) ([]llm.ContentBlock, error) {
				return []llm.ContentBlock{llm.NewTextBlock(string(value))}, nil
			}),
		},
		Executor: tools.ExecutorFunc(func(_ json.RawMessage, runContext tools.ToolRunContext) (json.RawMessage, error) {
			close(bodyStarted)
			<-runContext.Context.Done()
			close(bodyCanceled)
			<-releaseBody
			return nil, context.Cause(runContext.Context)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := state.loop.Create(
		context.Background(), state.pluginScope, "cancel-cause",
		agent.Options{Provider: "mock", Model: "model"}, session.Metadata{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Subject.Followup(userMessage(t, "run", llm.UserMessageSource{})); err != nil {
		t.Fatal(err)
	}
	<-bodyStarted
	handle.Subject.Cancel(agent.UserCancel{}, agent.CancelOptions{KeepInbox: true})
	<-bodyCanceled
	disposeDone := make(chan error, 1)
	go func() {
		disposeDone <- handle.Dispose(context.Background())
	}()
	select {
	case err := <-disposeDone:
		t.Fatalf("disposal returned before the active body drained: %v", err)
	default:
	}
	close(releaseBody)
	select {
	case err := <-disposeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("disposal did not drain canceled work")
	}
	ending := lastTurnEnd(t, handle.Subject.SessionValue())
	if ending.Kind != "aborted" || ending.Reason == nil || ending.Reason.Kind != "user" {
		t.Fatalf("turn ending = %#v, want first user cancellation", ending)
	}
	select {
	case durabilityErr := <-durabilityErrors:
		if durabilityErr != nil {
			t.Fatalf("turn durability Context = %v, want usable Context after cancellation", durabilityErr)
		}
	default:
		t.Fatal("canceled turn did not reach the durability checkpoint")
	}
}

func TestRequestErrorRetryRepeatsAttemptInsideOneStep(t *testing.T) {
	state := newHarnessFixture(t, [][]llm.StreamChunk{
		{llm.FinishChunk{Reason: llm.ErrorFinish{Failure: llm.LlmFailure{
			Message: "temporary", Code: "UPSTREAM",
		}}}},
		{
			llm.BlockEndChunk{Index: 0, Block: llm.NewTextBlock("recovered")},
			llm.FinishChunk{Reason: llm.StopFinish{}},
		},
	})
	requestErrors := make(chan agent.RequestErrorNotice, 1)
	if _, err := agent.OnRequestError(state.pluginScope,
		func(_ context.Context, notice agent.RequestErrorNotice, _ agent.RequestErrorNext) (agent.RequestErrorAction, error) {
			requestErrors <- notice
			return agent.RequestErrorAction{Retry: true}, nil
		}); err != nil {
		t.Fatal(err)
	}
	handle, err := state.loop.Create(
		context.Background(), state.pluginScope, "request-retry",
		agent.Options{Provider: "mock", Model: "model"}, session.Metadata{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if disposeErr := handle.Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	}()
	if err := handle.Subject.Followup(userMessage(t, "retry", llm.UserMessageSource{})); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := handle.Subject.WhenIdle(waitContext); err != nil {
		t.Fatal(err)
	}
	if requests := state.modelAdapter.snapshots(); len(requests) != 2 {
		t.Fatalf("model request count = %d, want 2 attempts", len(requests))
	}
	select {
	case notice := <-requestErrors:
		if notice.Turn != 1 || notice.Step != 1 || notice.Provider != "mock" || notice.Failure.Code != "UPSTREAM" {
			t.Fatalf("request-error notice = %#v", notice)
		}
	default:
		t.Fatal("request-error listener was not invoked")
	}
	ending := lastTurnEnd(t, handle.Subject.SessionValue())
	if ending.Kind != "completed" {
		t.Fatalf("turn ending = %#v, want completed", ending)
	}
}

type turnEndObservation struct {
	Kind   string `json:"kind"`
	Reason *struct {
		Kind string `json:"kind"`
	} `json:"reason,omitempty"`
}

func lastTurnEnd(t *testing.T, conversation *session.Session) turnEndObservation {
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

func lifecycleSnapshot(lock *sync.Mutex, entries []string) []string {
	lock.Lock()
	defer lock.Unlock()
	return append([]string(nil), entries...)
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

func toolResultCallID(message llm.Message) llm.CallID {
	blocks := message.ContentValue()
	if len(blocks) != 1 {
		return ""
	}
	resultBlock, ok := blocks[0].(llm.ToolResultBlock)
	if !ok {
		return ""
	}
	return resultBlock.ToolCallID
}
