//go:build contract

package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
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

type agentLoopContractState struct {
	engine        *plugin.Runtime
	providerScope *plugin.Scope
	models        llm.LlmRuntime
	toolRuntime   tools.ToolRuntime
	loopRuntime   agentloop.Loop
	modelAdapter  *agentLoopContractAdapter
	decorateTools func(tools.ToolRuntime) tools.ToolRuntime
	parallelLimit int
}

type agentLoopContractProvider struct {
	state *agentLoopContractState
}

func (*agentLoopContractProvider) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "agent-loop-contract",
		Provides: []plugin.ServiceRef{
			agent.Service.Ref(), session.StoreService.Ref(), llm.Service.Ref(),
			systemprompt.Service.Ref(), tools.Service.Ref(), agentloop.Service.Ref(),
		},
	}
}

func (provider *agentLoopContractProvider) Apply(requestContext context.Context, providerScope *plugin.Scope) error {
	agentRegistry, err := agent.NewRegistry(providerScope, agent.RegistryOptions{})
	if err != nil {
		return err
	}
	sessionStore, err := session.NewMemoryStore(providerScope, session.MemoryStoreOptions{})
	if err != nil {
		return err
	}
	modelRuntime, err := llm.NewRuntime(providerScope, nil)
	if err != nil {
		return err
	}
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		return err
	}
	promptRuntime, err := systemprompt.New(requestContext, providerScope, promptSettings)
	if err != nil {
		return err
	}
	toolSettings, err := tools.ValidateConfig(tools.Config{})
	if err != nil {
		return err
	}
	toolRuntime, err := tools.New(requestContext, providerScope, promptRuntime, nil, nil, toolSettings)
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
	loopRuntime, err := agentloop.New(requestContext, providerScope, agentloop.Dependencies{
		Agents: agentRegistry, Sessions: sessionStore, LLM: modelRuntime,
		Tools: loopTools, SystemPrompt: promptRuntime,
	}, loopSettings, agentloop.RuntimeOptions{})
	if err != nil {
		return err
	}
	provideOperations := []func() error{
		func() error {
			_, provideErr := plugin.Provide(providerScope, agent.Service, agentRegistry)
			return provideErr
		},
		func() error {
			_, provideErr := plugin.Provide(providerScope, session.StoreService, session.Store(sessionStore))
			return provideErr
		},
		func() error {
			_, provideErr := plugin.Provide(providerScope, llm.Service, modelRuntime)
			return provideErr
		},
		func() error {
			_, provideErr := plugin.Provide(providerScope, systemprompt.Service, promptRuntime)
			return provideErr
		},
		func() error {
			_, provideErr := plugin.Provide(providerScope, tools.Service, toolRuntime)
			return provideErr
		},
		func() error {
			_, provideErr := plugin.Provide(providerScope, agentloop.Service, loopRuntime)
			return provideErr
		},
	}
	for _, provideOperation := range provideOperations {
		if err := provideOperation(); err != nil {
			return err
		}
	}
	provider.state.providerScope = providerScope
	provider.state.models = modelRuntime
	provider.state.toolRuntime = toolRuntime
	provider.state.loopRuntime = loopRuntime
	return nil
}

type agentLoopContractAdapter struct {
	mu       sync.Mutex
	requests []llm.GenerateOptions
}

func (backend *agentLoopContractAdapter) Stream(_ context.Context, options llm.GenerateOptions) (llm.ChunkStream, error) {
	backend.mu.Lock()
	requestSnapshot, err := llm.CloneGenerateOptions(options)
	if err != nil {
		backend.mu.Unlock()
		return nil, err
	}
	backend.requests = append(backend.requests, requestSnapshot)
	requestIndex := len(backend.requests) - 1
	backend.mu.Unlock()
	switch requestIndex {
	case 0:
		return llm.NewSliceStream([]llm.StreamChunk{
			llm.BlockEndChunk{Index: 0, Block: llm.ToolCallBlock{
				ID: "call-1", Name: "echo", Arguments: `{"value":"hello"}`,
			}},
			llm.FinishChunk{Reason: llm.ToolCallsFinish{}},
		})
	case 1:
		return llm.NewSliceStream([]llm.StreamChunk{
			llm.BlockEndChunk{Index: 0, Block: llm.NewTextBlock("done")},
			llm.FinishChunk{Reason: llm.StopFinish{}},
		})
	default:
		return nil, errors.New("agent-loop contract adapter has no scripted response")
	}
}

func (backend *agentLoopContractAdapter) snapshots() []llm.GenerateOptions {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	result := make([]llm.GenerateOptions, 0, len(backend.requests))
	for _, requestSnapshot := range backend.requests {
		detached, _ := llm.CloneGenerateOptions(requestSnapshot)
		result = append(result, detached)
	}
	return result
}

type agentLoopMessageObservation struct {
	Role    llm.MessageRole    `json:"role"`
	Content []llm.ContentBlock `json:"content"`
	Source  llm.MessageSource  `json:"source"`
}

type agentLoopEventObservation struct {
	Type string          `json:"type"`
	Seq  int64           `json:"seq"`
	Data json.RawMessage `json:"data"`
}

type agentLoopRequestObservation struct {
	Provider  string                        `json:"provider"`
	Model     string                        `json:"model"`
	System    *string                       `json:"system,omitempty"`
	Tools     []string                      `json:"tools"`
	Messages  []agentLoopMessageObservation `json:"messages"`
	SessionID string                        `json:"sessionId"`
}

type agentLoopContractObservation struct {
	Events   []agentLoopEventObservation   `json:"events"`
	Requests []agentLoopRequestObservation `json:"requests"`
	Derived  []agentLoopMessageObservation `json:"derived"`
}

type agentLoopInboxObservation struct {
	Target       agent.InboxTarget             `json:"target"`
	Start        int                           `json:"start"`
	RemovedCount *int                          `json:"removedCount,omitempty"`
	Inserted     []agentLoopMessageObservation `json:"inserted"`
	Outcome      agent.InboxOutcome            `json:"outcome,omitempty"`
}

type agentLoopMessageEventObservation struct {
	Turn    int64                       `json:"turn"`
	Step    int64                       `json:"step"`
	Message agentLoopMessageObservation `json:"message"`
	Usage   *llm.TokenUsage             `json:"usage,omitempty"`
	Error   *session.ToolErrorInfo      `json:"error,omitempty"`
	Meta    json.RawMessage             `json:"meta,omitempty"`
}

type agentLoopHeaderObservation struct {
	Config          llm.CallConfig                 `json:"config"`
	AdapterDefaults *llm.CallConfigAdapterDefaults `json:"adapterDefaults,omitempty"`
	System          *string                        `json:"system,omitempty"`
	Tools           []string                       `json:"tools"`
}

type agentLoopHeaderEventObservation struct {
	Header agentLoopHeaderObservation  `json:"header"`
	Reason session.RequestHeaderReason `json:"reason"`
}

func TestPinnedSourceAgentLoopMatchesGo(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	commandContext, cancelCommand := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCommand()
	sourceOutput, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "agent-loop.ts"),
		sourceRoot, filepath.Join(repositoryRoot, "contracts", "deepseek-harness", "manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}

	contractState := &agentLoopContractState{
		engine: plugin.NewRuntime(), modelAdapter: &agentLoopContractAdapter{},
	}
	if _, err := contractState.engine.Load(context.Background(), &agentLoopContractProvider{state: contractState}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := contractState.engine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if _, err := contractState.models.RegisterAdapter(
		context.Background(), contractState.providerScope, []string{"mock"}, contractState.modelAdapter,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := contractState.toolRuntime.Register(context.Background(), contractState.providerScope, tools.ToolDefinition{
		Name: "echo", Description: "echo one object", Parameters: json.RawMessage(`{"type":"object"}`),
		Output: tools.ToolOutputDefinition{
			Schema: json.RawMessage(`{"type":"object"}`),
			Renderer: tools.OutputRendererFunc(func(_ json.RawMessage, value json.RawMessage) ([]llm.ContentBlock, error) {
				return []llm.ContentBlock{llm.NewTextBlock(string(value))}, nil
			}),
		},
		Executor: tools.ExecutorFunc(func(arguments json.RawMessage, _ tools.ToolRunContext) (json.RawMessage, error) {
			return append(json.RawMessage(nil), arguments...), nil
		}),
	}); err != nil {
		t.Fatal(err)
	}
	userInput, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock("hello")}, Source: llm.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := contractState.loopRuntime.Create(
		context.Background(), contractState.providerScope, "agent-loop-contract",
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
	if err := handle.Subject.Followup(userInput); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := handle.Subject.WhenIdle(waitContext); err != nil {
		t.Fatal(err)
	}

	observation := agentLoopContractObservation{}
	for _, event := range handle.Subject.SessionValue().Events() {
		observation.Events = append(observation.Events, projectAgentLoopEvent(t, event))
	}
	for _, requestSnapshot := range contractState.modelAdapter.snapshots() {
		observation.Requests = append(observation.Requests, projectAgentLoopRequest(requestSnapshot))
	}
	derivedMessages, err := handle.Subject.SessionValue().DeriveMessages()
	if err != nil {
		t.Fatal(err)
	}
	observation.Derived = projectAgentLoopMessages(derivedMessages)
	goOutput, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, goOutput, sourceOutput)
}

func projectAgentLoopEvent(t *testing.T, event session.Event) agentLoopEventObservation {
	t.Helper()
	projected := agentLoopEventObservation{Type: event.Type, Seq: event.Seq, Data: append(json.RawMessage(nil), event.Data...)}
	var err error
	switch event.Type {
	case "agent/inbox/spliced":
		var mutation agent.InboxSplice
		if err = json.Unmarshal(event.Data, &mutation); err == nil {
			projected.Data, err = json.Marshal(agentLoopInboxObservation{
				Target: mutation.Target, Start: mutation.Start, RemovedCount: mutation.RemovedCount,
				Inserted: projectAgentLoopUserMessages(mutation.Inserted), Outcome: mutation.Outcome,
			})
		}
	case session.UserMessageEventName:
		var messageValue llm.UserMessage
		messageValue, err = llm.DecodeUserMessage(event.Data)
		if err == nil {
			projected.Data, err = json.Marshal(projectAgentLoopMessage(messageValue))
		}
	case session.AssistantMessageEventName:
		var payload struct {
			Turn    int64           `json:"turn"`
			Step    int64           `json:"step"`
			Message json.RawMessage `json:"message"`
			Usage   *llm.TokenUsage `json:"usage,omitempty"`
		}
		if err = json.Unmarshal(event.Data, &payload); err == nil {
			var messageValue llm.Message
			messageValue, err = llm.DecodeMessage(payload.Message)
			if err == nil {
				projected.Data, err = json.Marshal(agentLoopMessageEventObservation{
					Turn: payload.Turn, Step: payload.Step,
					Message: projectAgentLoopMessage(messageValue), Usage: payload.Usage,
				})
			}
		}
	case session.ToolResultEventName:
		var payload struct {
			Turn    int64                  `json:"turn"`
			Step    int64                  `json:"step"`
			Message json.RawMessage        `json:"message"`
			Error   *session.ToolErrorInfo `json:"error,omitempty"`
			Meta    json.RawMessage        `json:"meta,omitempty"`
		}
		if err = json.Unmarshal(event.Data, &payload); err == nil {
			var messageValue llm.Message
			messageValue, err = llm.DecodeMessage(payload.Message)
			if err == nil {
				projected.Data, err = json.Marshal(agentLoopMessageEventObservation{
					Turn: payload.Turn, Step: payload.Step, Message: projectAgentLoopMessage(messageValue),
					Error: payload.Error, Meta: payload.Meta,
				})
			}
		}
	case session.RequestHeaderEventName:
		var payload session.RequestHeaderSnapshot
		if err = json.Unmarshal(event.Data, &payload); err == nil {
			toolNames := make([]string, len(payload.Header.Tools))
			for toolIndex, schema := range payload.Header.Tools {
				toolNames[toolIndex] = schema.Name
			}
			projected.Data, err = json.Marshal(agentLoopHeaderEventObservation{
				Header: agentLoopHeaderObservation{
					Config: payload.Header.Config, AdapterDefaults: payload.Header.AdapterDefaults,
					System: payload.Header.System, Tools: toolNames,
				},
				Reason: payload.Reason,
			})
		}
	}
	if err != nil {
		t.Fatalf("project Agent Loop event %q: %v", event.Type, err)
	}
	return projected
}

func projectAgentLoopRequest(requestSnapshot llm.GenerateOptions) agentLoopRequestObservation {
	toolNames := make([]string, len(requestSnapshot.Tools))
	for toolIndex, schema := range requestSnapshot.Tools {
		toolNames[toolIndex] = schema.Name
	}
	return agentLoopRequestObservation{
		Provider: requestSnapshot.Provider, Model: requestSnapshot.Model,
		System: requestSnapshot.System, Tools: toolNames,
		Messages: projectAgentLoopMessages(requestSnapshot.Messages), SessionID: requestSnapshot.SessionID,
	}
}

func projectAgentLoopUserMessages(messages []llm.UserMessage) []agentLoopMessageObservation {
	projected := make([]agentLoopMessageObservation, len(messages))
	for messageIndex, messageValue := range messages {
		projected[messageIndex] = projectAgentLoopMessage(messageValue)
	}
	return projected
}

func projectAgentLoopMessages(messages []llm.Message) []agentLoopMessageObservation {
	projected := make([]agentLoopMessageObservation, len(messages))
	for messageIndex, messageValue := range messages {
		projected[messageIndex] = projectAgentLoopMessage(messageValue)
	}
	return projected
}

func projectAgentLoopMessage(messageValue llm.Message) agentLoopMessageObservation {
	return agentLoopMessageObservation{
		Role: messageValue.ConversationRole(), Content: messageValue.ContentValue(), Source: messageValue.SourceValue(),
	}
}
