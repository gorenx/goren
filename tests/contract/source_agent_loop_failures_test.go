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
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

type agentLoopFailureAdapter struct {
	mu           sync.Mutex
	chunks       []llm.StreamChunk
	requestCount int
}

func (backend *agentLoopFailureAdapter) Stream(context.Context, llm.GenerateOptions) (llm.ChunkStream, error) {
	backend.mu.Lock()
	backend.requestCount++
	backend.mu.Unlock()
	return llm.NewSliceStream(backend.chunks)
}

func (backend *agentLoopFailureAdapter) requests() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.requestCount
}

type agentLoopFailureTurnEnd struct {
	Kind  string          `json:"kind"`
	Error *llm.LlmFailure `json:"error,omitempty"`
}

type agentLoopFailureScenario struct {
	EventTypes   []string                `json:"eventTypes"`
	TurnEnd      agentLoopFailureTurnEnd `json:"turnEnd"`
	RequestCount int                     `json:"requestCount"`
}

type agentLoopSchedulerFailureScenario struct {
	agentLoopFailureScenario
	Started           []string     `json:"started"`
	IdleBeforeRelease bool         `json:"idleBeforeRelease"`
	ToolCallIDs       []llm.CallID `json:"toolCallIds"`
	ToolResultCount   int          `json:"toolResultCount"`
}

type agentLoopFailuresObservation struct {
	PreStepReject    agentLoopFailureScenario          `json:"preStepReject"`
	ModelFailure     agentLoopFailureScenario          `json:"modelFailure"`
	SchedulerFailure agentLoopSchedulerFailureScenario `json:"schedulerFailure"`
}

type agentLoopInjectedRuntime struct {
	tools.ToolRuntime
	staged tools.ToolExecutionScheduler
}

func (carrier *agentLoopInjectedRuntime) Scheduler() tools.ToolExecutionScheduler {
	return carrier.staged
}

type agentLoopDispatchFailureScheduler struct {
	delegate        tools.ToolExecutionScheduler
	failureCallID   llm.CallID
	waitForStarted  <-chan struct{}
	failureReturned chan<- struct{}
}

func (bridge *agentLoopDispatchFailureScheduler) Prepare(
	requestContext context.Context,
	input tools.ToolExecutionInput,
) (tools.ScheduledToolPreparation, error) {
	return bridge.delegate.Prepare(requestContext, input)
}

func (bridge *agentLoopDispatchFailureScheduler) Dispatch(
	execution tools.ToolExecution,
) (tools.ScheduledToolDispatch, error) {
	if execution.CallID != bridge.failureCallID {
		return bridge.delegate.Dispatch(execution)
	}
	<-bridge.waitForStarted
	close(bridge.failureReturned)
	return tools.ScheduledToolDispatch{}, errors.New("scheduler failed")
}

func (bridge *agentLoopDispatchFailureScheduler) Finalize(
	execution tools.ToolExecution,
	outcome tools.ToolExecutionResult,
) (tools.ToolExecutionResult, error) {
	return bridge.delegate.Finalize(execution, outcome)
}

func (bridge *agentLoopDispatchFailureScheduler) Finish(
	execution tools.ToolExecution,
	outcome tools.ToolExecutionResult,
) tools.ToolExecutionResult {
	return bridge.delegate.Finish(execution, outcome)
}

func TestPinnedSourceAgentLoopFailuresMatchGo(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	commandContext, cancelCommand := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCommand()
	sourceOutput, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "agent-loop-failures.ts"),
		sourceRoot, filepath.Join(repositoryRoot, "contracts", "deepseek-harness", "manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}

	preStepChunks := []llm.StreamChunk{llm.FinishChunk{Reason: llm.StopFinish{}}}
	preStepState, preStepAdapter := newAgentLoopFailureState(t, preStepChunks, 0, nil)
	if _, err := agent.OnPreStep(preStepState.providerScope,
		func(context.Context, agent.PreStepNotice, agent.PreStepNext) (agent.PreStepDecision, error) {
			return agent.PreStepDecision{Kind: agent.PreStepReject}, nil
		}); err != nil {
		t.Fatal(err)
	}
	preStepReject := runAgentLoopFailureScenario(t, preStepState, preStepAdapter, "failure-pre-step")

	modelChunks := []llm.StreamChunk{llm.FinishChunk{Reason: llm.ErrorFinish{Failure: llm.LlmFailure{
		Message: "model failed", Code: "MODEL_FAILURE",
	}}}}
	modelState, modelAdapter := newAgentLoopFailureState(t, modelChunks, 0, nil)
	modelFailure := runAgentLoopFailureScenario(t, modelState, modelAdapter, "failure-model")

	schedulerFailure := runAgentLoopSchedulerFailureScenario(t)
	goOutput, err := json.Marshal(agentLoopFailuresObservation{
		PreStepReject: preStepReject, ModelFailure: modelFailure, SchedulerFailure: schedulerFailure,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, goOutput, sourceOutput)
}

func newAgentLoopFailureState(
	t *testing.T,
	chunks []llm.StreamChunk,
	parallelLimit int,
	decorateTools func(tools.ToolRuntime) tools.ToolRuntime,
) (*agentLoopContractState, *agentLoopFailureAdapter) {
	t.Helper()
	backend := &agentLoopFailureAdapter{chunks: chunks}
	contractState := &agentLoopContractState{
		engine: plugin.NewRuntime(), parallelLimit: parallelLimit, decorateTools: decorateTools,
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
		context.Background(), contractState.providerScope, []string{"mock"}, backend,
	); err != nil {
		t.Fatal(err)
	}
	return contractState, backend
}

func runAgentLoopFailureScenario(
	t *testing.T,
	contractState *agentLoopContractState,
	backend *agentLoopFailureAdapter,
	identifier session.SessionID,
) agentLoopFailureScenario {
	t.Helper()
	handle, err := contractState.loopRuntime.Create(
		context.Background(), contractState.providerScope, identifier,
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
	messageValue, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock("run")}, Source: llm.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Subject.Followup(messageValue); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := handle.Subject.WhenIdle(waitContext); err != nil {
		t.Fatal(err)
	}
	return projectAgentLoopFailureScenario(t, handle.Subject.SessionValue(), backend.requests())
}

func runAgentLoopSchedulerFailureScenario(t *testing.T) agentLoopSchedulerFailureScenario {
	t.Helper()
	chunks := []llm.StreamChunk{
		llm.BlockEndChunk{Index: 0, Block: llm.ToolCallBlock{ID: "call-1", Name: "parallel", Arguments: `{"id":"1"}`}},
		llm.BlockEndChunk{Index: 1, Block: llm.ToolCallBlock{ID: "call-2", Name: "parallel", Arguments: `{"id":"2"}`}},
		llm.BlockEndChunk{Index: 2, Block: llm.ToolCallBlock{ID: "call-3", Name: "parallel", Arguments: `{"id":"3"}`}},
		llm.FinishChunk{Reason: llm.ToolCallsFinish{}},
	}
	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	failureReturned := make(chan struct{})
	contractState, backend := newAgentLoopFailureState(t, chunks, 2, func(baseRuntime tools.ToolRuntime) tools.ToolRuntime {
		return &agentLoopInjectedRuntime{
			ToolRuntime: baseRuntime,
			staged: &agentLoopDispatchFailureScheduler{
				delegate: baseRuntime.Scheduler(), failureCallID: "call-1",
				waitForStarted: secondStarted, failureReturned: failureReturned,
			},
		}
	})
	var startedMu sync.Mutex
	started := make([]string, 0, 3)
	if _, err := contractState.toolRuntime.Register(context.Background(), contractState.providerScope, tools.ToolDefinition{
		Name: "parallel", Description: "parallel failure fixture",
		Parameters: json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"string"}}}`),
		Output: tools.ToolOutputDefinition{
			Schema: json.RawMessage(`{"type":"object"}`),
			Renderer: tools.OutputRendererFunc(func(_ json.RawMessage, value json.RawMessage) ([]llm.ContentBlock, error) {
				return []llm.ContentBlock{llm.NewTextBlock(string(value))}, nil
			}),
		},
		ConcurrencyBehavior: tools.ConcurrencyClassifierFunc(func(json.RawMessage) bool { return true }),
		Executor: tools.ExecutorFunc(func(arguments json.RawMessage, _ tools.ToolRunContext) (json.RawMessage, error) {
			var input struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(arguments, &input); err != nil {
				return nil, err
			}
			startedMu.Lock()
			started = append(started, input.ID)
			startedMu.Unlock()
			if input.ID == "2" {
				close(secondStarted)
				<-releaseSecond
			}
			return append(json.RawMessage(nil), arguments...), nil
		}),
	}); err != nil {
		t.Fatal(err)
	}
	handle, err := contractState.loopRuntime.Create(
		context.Background(), contractState.providerScope, "failure-scheduler",
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
	messageValue, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock("run")}, Source: llm.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Subject.Followup(messageValue); err != nil {
		t.Fatal(err)
	}
	idleDone := make(chan error, 1)
	go func() {
		idleDone <- handle.Subject.WhenIdle(context.Background())
	}()
	select {
	case <-failureReturned:
	case <-time.After(time.Second):
		t.Fatal("scheduler failure did not observe the started sibling")
	}
	idleBeforeRelease := false
	select {
	case idleErr := <-idleDone:
		if idleErr != nil {
			t.Fatal(idleErr)
		}
		idleBeforeRelease = true
	default:
	}
	close(releaseSecond)
	if !idleBeforeRelease {
		select {
		case idleErr := <-idleDone:
			if idleErr != nil {
				t.Fatal(idleErr)
			}
		case <-time.After(time.Second):
			t.Fatal("scheduler failure did not settle after drain")
		}
	}
	projected := projectAgentLoopFailureScenario(t, handle.Subject.SessionValue(), backend.requests())
	toolCallIDs := make([]llm.CallID, 0, 2)
	toolResultCount := 0
	for _, event := range handle.Subject.SessionValue().Events() {
		switch event.Type {
		case session.ToolCallEventName:
			var callValue session.ToolCall
			if err := json.Unmarshal(event.Data, &callValue); err != nil {
				t.Fatal(err)
			}
			toolCallIDs = append(toolCallIDs, callValue.CallID)
		case session.ToolResultEventName:
			toolResultCount++
		}
	}
	startedMu.Lock()
	startedSnapshot := append([]string(nil), started...)
	startedMu.Unlock()
	return agentLoopSchedulerFailureScenario{
		agentLoopFailureScenario: projected,
		Started:                  startedSnapshot, IdleBeforeRelease: idleBeforeRelease,
		ToolCallIDs: toolCallIDs, ToolResultCount: toolResultCount,
	}
}

func projectAgentLoopFailureScenario(
	t *testing.T,
	conversation *session.Session,
	requestCount int,
) agentLoopFailureScenario {
	t.Helper()
	projected := agentLoopFailureScenario{RequestCount: requestCount}
	for _, event := range conversation.Events() {
		projected.EventTypes = append(projected.EventTypes, event.Type)
		if event.Type != session.TurnEndEventName {
			continue
		}
		var payload struct {
			Reason agentLoopFailureTurnEnd `json:"reason"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatal(err)
		}
		projected.TurnEnd = payload.Reason
	}
	return projected
}
