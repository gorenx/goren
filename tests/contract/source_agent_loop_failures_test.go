//go:build contract

package contract_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

type agentLoopFailureAdapter struct {
	mutex        sync.Mutex
	chunks       []llm.StreamChunk
	requestCount int
}

func (backend *agentLoopFailureAdapter) Stream(
	context.Context,
	llm.GenerateOptions,
) (llm.ChunkStream, error) {
	backend.mutex.Lock()
	backend.requestCount++
	backend.mutex.Unlock()
	return llm.NewSliceStream(backend.chunks)
}

func (backend *agentLoopFailureAdapter) requests() int {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
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

type agentLoopFailuresObservation struct {
	PreStepReject agentLoopFailureScenario `json:"preStepReject"`
	ModelFailure  agentLoopFailureScenario `json:"modelFailure"`
}

func TestPinnedSourceAgentLoopFailuresMatchGo(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	commandContext, cancelCommand := context.WithTimeout(
		context.Background(),
		30*time.Second,
	)
	defer cancelCommand()
	sourceOutput, err := runTypeScript(
		commandContext,
		sourceRoot,
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

	preStepChunks := []llm.StreamChunk{
		llm.FinishChunk{
			Reason: llm.StopFinish{},
		},
	}
	preStepState, preStepAdapter := newAgentLoopFailureState(
		t,
		preStepChunks,
		0,
	)
	preStepMiddleware := &contractWaterfallPlugin[
		agent.PreStepNotice,
		agent.PreStepDecision,
	]{
		name: "failure-pre-step-policy",
		middleware: contractWaterfallFunc[
			agent.PreStepNotice,
			agent.PreStepDecision,
		](func(
			context.Context,
			agent.PreStepNotice,
			plugin.WaterfallAction[
				agent.PreStepNotice,
				agent.PreStepDecision,
			],
		) (agent.PreStepDecision, error) {
			return agent.PreStepDecision{
				Kind: agent.PreStepReject,
			}, nil
		}),
	}
	if _, err = preStepState.engine.Mount(
		context.Background(),
		preStepMiddleware,
	); err != nil {
		t.Fatal(err)
	}
	preStepReject := runAgentLoopFailureScenario(
		t,
		preStepState,
		preStepAdapter,
		"failure-pre-step",
	)

	modelChunks := []llm.StreamChunk{
		llm.FinishChunk{
			Reason: llm.ErrorFinish{
				Failure: llm.LlmFailure{
					Message: "model failed",
					Code:    "MODEL_FAILURE",
				},
			},
		},
	}
	modelState, modelAdapter := newAgentLoopFailureState(t, modelChunks, 0)
	modelFailure := runAgentLoopFailureScenario(
		t,
		modelState,
		modelAdapter,
		"failure-model",
	)

	var sourceObservation agentLoopFailuresObservation
	if err = json.Unmarshal(sourceOutput, &sourceObservation); err != nil {
		t.Fatal(err)
	}
	goObservation := agentLoopFailuresObservation{
		PreStepReject: preStepReject,
		ModelFailure:  modelFailure,
	}
	if !reflect.DeepEqual(goObservation, sourceObservation) {
		t.Fatalf(
			"Agent Loop failures = %#v, want %#v",
			goObservation,
			sourceObservation,
		)
	}
}

func newAgentLoopFailureState(
	testingContext *testing.T,
	chunks []llm.StreamChunk,
	parallelLimit int,
) (*agentLoopContractState, *agentLoopFailureAdapter) {
	testingContext.Helper()
	backend := &agentLoopFailureAdapter{
		chunks: chunks,
	}
	contractState := &agentLoopContractState{
		parallelLimit: parallelLimit,
	}
	if err := startAgentLoopContractState(
		testingContext,
		contractState,
	); err != nil {
		testingContext.Fatal(err)
	}
	testingContext.Cleanup(func() {
		if err := contractState.engine.Shutdown(context.Background()); err != nil {
			testingContext.Error(err)
		}
	})
	if _, err := contractState.models.RegisterAdapter(
		context.Background(),
		[]string{
			"mock",
		},
		backend,
	); err != nil {
		testingContext.Fatal(err)
	}
	return contractState, backend
}

func runAgentLoopFailureScenario(
	testingContext *testing.T,
	contractState *agentLoopContractState,
	backend *agentLoopFailureAdapter,
	identifier session.SessionID,
) agentLoopFailureScenario {
	testingContext.Helper()
	handle, err := contractState.agents.Create(
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
		testingContext.Fatal(err)
	}
	defer func() {
		if disposeErr := handle.Dispose(context.Background()); disposeErr != nil {
			testingContext.Error(disposeErr)
		}
	}()
	messageValue, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{
			llm.NewTextBlock("run"),
		},
		Source: llm.UserMessageSource{},
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	if err := handle.Subject.Followup(messageValue); err != nil {
		testingContext.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer cancelWait()
	if err := handle.Subject.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
	return projectAgentLoopFailureScenario(
		testingContext,
		handle.Subject.SessionValue(),
		backend.requests(),
	)
}

func projectAgentLoopFailureScenario(
	testingContext *testing.T,
	conversation *session.Session,
	requestCount int,
) agentLoopFailureScenario {
	testingContext.Helper()
	projected := agentLoopFailureScenario{
		RequestCount: requestCount,
	}
	for _, committed := range conversation.Events() {
		projected.EventTypes = append(projected.EventTypes, committed.Type)
		if committed.Type != session.TurnEndEventName {
			continue
		}
		var payload struct {
			Reason agentLoopFailureTurnEnd `json:"reason"`
		}
		if err := json.Unmarshal(committed.Data, &payload); err != nil {
			testingContext.Fatal(err)
		}
		projected.TurnEnd = payload.Reason
	}
	return projected
}
