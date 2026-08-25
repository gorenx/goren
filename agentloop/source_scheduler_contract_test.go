//go:build contract

package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	contractfixture "github.com/gorenx/goren/tests/contract/fixture"
	"github.com/gorenx/goren/tools"
)

type schedulerContractFailureReporter struct {
	testingContext *testing.T
}

func (reporter schedulerContractFailureReporter) ReportEventFailure(
	_ context.Context,
	failure plugin.EventFailure,
) {
	reporter.testingContext.Errorf(
		"best-effort Event %q delivery failed: %v",
		failure.EventName,
		failure.Error,
	)
}

func (reporter schedulerContractFailureReporter) ReportPostCommitFailure(
	failure session.PostCommitFailure,
) {
	reporter.testingContext.Errorf(
		"Session %q post-commit failure: %v",
		failure.SessionID,
		failure.Error,
	)
}

type schedulerContractAdapter struct {
	mutex        sync.Mutex
	chunks       []llm.StreamChunk
	requestCount int
}

func (backend *schedulerContractAdapter) Stream(
	context.Context,
	llm.GenerateOptions,
) (llm.ChunkStream, error) {
	backend.mutex.Lock()
	backend.requestCount++
	backend.mutex.Unlock()
	return llm.NewSliceStream(backend.chunks)
}

func (backend *schedulerContractAdapter) requests() int {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	return backend.requestCount
}

type schedulerContractRuntime struct {
	tools.ToolRuntime
	scheduler tools.ToolExecutionScheduler
}

func (runtimeState *schedulerContractRuntime) Scheduler() tools.ToolExecutionScheduler {
	return runtimeState.scheduler
}

type schedulerContractFailure struct {
	delegate        tools.ToolExecutionScheduler
	failureCallID   llm.CallID
	waitForStarted  <-chan struct{}
	failureReturned chan<- struct{}
}

func (schedulerState *schedulerContractFailure) Prepare(
	requestContext context.Context,
	input tools.ToolExecutionInput,
) (tools.ScheduledToolPreparation, error) {
	return schedulerState.delegate.Prepare(requestContext, input)
}

func (schedulerState *schedulerContractFailure) Dispatch(
	execution tools.ToolExecution,
) (tools.ScheduledToolDispatch, error) {
	if execution.CallID != schedulerState.failureCallID {
		return schedulerState.delegate.Dispatch(execution)
	}
	<-schedulerState.waitForStarted
	close(schedulerState.failureReturned)
	return tools.ScheduledToolDispatch{}, errors.New("scheduler failed")
}

func (schedulerState *schedulerContractFailure) Finalize(
	execution tools.ToolExecution,
	outcome tools.ToolExecutionResult,
) (tools.ToolExecutionResult, error) {
	return schedulerState.delegate.Finalize(execution, outcome)
}

func (schedulerState *schedulerContractFailure) Finish(
	execution tools.ToolExecution,
	outcome tools.ToolExecutionResult,
) tools.ToolExecutionResult {
	return schedulerState.delegate.Finish(execution, outcome)
}

type schedulerContractTurnEnd struct {
	Kind  string          `json:"kind"`
	Error *llm.LlmFailure `json:"error,omitempty"`
}

type schedulerContractObservation struct {
	EventTypes        []string                 `json:"eventTypes"`
	TurnEnd           schedulerContractTurnEnd `json:"turnEnd"`
	RequestCount      int                      `json:"requestCount"`
	Started           []string                 `json:"started"`
	IdleBeforeRelease bool                     `json:"idleBeforeRelease"`
	ToolCallIDs       []llm.CallID             `json:"toolCallIds"`
	ToolResultCount   int                      `json:"toolResultCount"`
}

func TestPinnedSourceSchedulerFailureMatchesGo(t *testing.T) {
	repositoryRoot, sourceRoot := contractfixture.Paths(t)
	commandContext, cancelCommand := context.WithTimeout(
		context.Background(),
		30*time.Second,
	)
	defer cancelCommand()
	sourceOutput, err := contractfixture.RunTypeScript(
		commandContext,
		sourceRoot,
		nil,
		filepath.Join(
			repositoryRoot,
			"tests",
			"contract",
			"typescript",
			"agent-loop-failures.ts",
		),
		sourceRoot,
		filepath.Join(
			repositoryRoot,
			"contracts",
			"deepseek-harness",
			"manifest.json",
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	var sourceObservation struct {
		SchedulerFailure schedulerContractObservation `json:"schedulerFailure"`
	}
	if err = json.Unmarshal(sourceOutput, &sourceObservation); err != nil {
		t.Fatal(err)
	}

	chunks := []llm.StreamChunk{
		llm.BlockEndChunk{
			Index: 0,
			Block: llm.ToolCallBlock{
				ID:        "call-1",
				Name:      "parallel",
				Arguments: `{"id":"1"}`,
			},
		},
		llm.BlockEndChunk{
			Index: 1,
			Block: llm.ToolCallBlock{
				ID:        "call-2",
				Name:      "parallel",
				Arguments: `{"id":"2"}`,
			},
		},
		llm.BlockEndChunk{
			Index: 2,
			Block: llm.ToolCallBlock{
				ID:        "call-3",
				Name:      "parallel",
				Arguments: `{"id":"3"}`,
			},
		},
		llm.FinishChunk{
			Reason: llm.ToolCallsFinish{},
		},
	}
	backend := &schedulerContractAdapter{
		chunks: chunks,
	}
	agents := agent.NewRegistry(agent.RegistryOptions{})
	failures := schedulerContractFailureReporter{
		testingContext: t,
	}
	sessions, err := session.NewMemoryStore(session.MemoryStoreOptions{
		PostCommitFailures: failures,
	}, sessionEventSinkStub{})
	if err != nil {
		t.Fatal(err)
	}
	models := llm.NewRuntime(nil)
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		t.Fatal(err)
	}
	prompts := systemprompt.New(
		promptSettings,
		systemprompt.RegistryOptions{},
	)
	toolSettings, err := tools.ValidateConfig(tools.Config{})
	if err != nil {
		t.Fatal(err)
	}
	toolRuntime := tools.New(toolSettings)
	loopRuntime, err := New(
		Settings{
			MaxParallelToolCalls: 2,
		},
		RuntimeOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: failures,
	})
	if _, err = runtimeEngine.Start(
		context.Background(),
		agents,
		sessions,
		models,
		prompts,
		toolRuntime,
		loopRuntime,
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if shutdownErr := runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})
	if _, err = models.RegisterAdapter(
		context.Background(),
		[]string{
			"mock",
		},
		backend,
	); err != nil {
		t.Fatal(err)
	}

	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	var releaseOnce sync.Once
	releaseSibling := func() {
		releaseOnce.Do(func() {
			close(releaseSecond)
		})
	}
	defer releaseSibling()
	failureReturned := make(chan struct{})
	var startedMutex sync.Mutex
	started := make([]string, 0, 3)
	if _, err = toolRuntime.AddTool(
		context.Background(),
		tools.ToolDefinition{
			Name:        "parallel",
			Description: "parallel failure fixture",
			Parameters: json.RawMessage(
				`{"type":"object","required":["id"],"properties":{"id":{"type":"string"}}}`,
			),
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
			ConcurrencyBehavior: tools.ConcurrencyClassifierFunc(
				func(json.RawMessage) bool {
					return true
				},
			),
			Executor: tools.ExecutorFunc(func(
				arguments json.RawMessage,
				_ tools.ToolRunContext,
			) (json.RawMessage, error) {
				var input struct {
					ID string `json:"id"`
				}
				if decodeErr := json.Unmarshal(arguments, &input); decodeErr != nil {
					return nil, decodeErr
				}
				startedMutex.Lock()
				started = append(started, input.ID)
				startedMutex.Unlock()
				if input.ID == "2" {
					close(secondStarted)
					<-releaseSecond
				}
				return append(json.RawMessage(nil), arguments...), nil
			}),
		},
	); err != nil {
		t.Fatal(err)
	}

	handle, err := agents.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: "failure-scheduler",
			AgentOptions: agent.Options{
				Provider: "mock",
				Model:    "model",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if disposeErr := handle.Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	}()
	subject, matches := handle.Subject.(*ReactLoopAgent)
	if !matches {
		t.Fatalf("Agent type = %T", handle.Subject)
	}
	toolCalls := subject.loop.turns.requests.toolCalls
	baseRuntime := toolCalls.toolRuntime
	toolCalls.toolRuntime = &schedulerContractRuntime{
		ToolRuntime: baseRuntime,
		scheduler: &schedulerContractFailure{
			delegate:        baseRuntime.Scheduler(),
			failureCallID:   "call-1",
			waitForStarted:  secondStarted,
			failureReturned: failureReturned,
		},
	}

	messageValue, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{
			llm.NewTextBlock("run"),
		},
		Source: llm.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = handle.Subject.Followup(messageValue); err != nil {
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
	releaseSibling()
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

	goObservation := schedulerContractObservation{
		RequestCount:      backend.requests(),
		IdleBeforeRelease: idleBeforeRelease,
	}
	for _, committed := range handle.Subject.SessionValue().Events() {
		goObservation.EventTypes = append(
			goObservation.EventTypes,
			committed.Type,
		)
		switch committed.Type {
		case session.TurnEndEventName:
			var payload struct {
				Reason schedulerContractTurnEnd `json:"reason"`
			}
			if decodeErr := json.Unmarshal(committed.Data, &payload); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			goObservation.TurnEnd = payload.Reason
		case session.ToolCallEventName:
			var callValue session.ToolCall
			if decodeErr := json.Unmarshal(committed.Data, &callValue); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			goObservation.ToolCallIDs = append(
				goObservation.ToolCallIDs,
				callValue.CallID,
			)
		case session.ToolResultEventName:
			goObservation.ToolResultCount++
		}
	}
	startedMutex.Lock()
	goObservation.Started = append([]string(nil), started...)
	startedMutex.Unlock()
	if !reflect.DeepEqual(goObservation, sourceObservation.SchedulerFailure) {
		t.Fatalf(
			"scheduler failure = %#v, want %#v",
			goObservation,
			sourceObservation.SchedulerFailure,
		)
	}
}
