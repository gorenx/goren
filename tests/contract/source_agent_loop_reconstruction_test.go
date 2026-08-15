//go:build contract

package contract_test

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
)

type reconstructionAdapter struct {
	mu         sync.Mutex
	response   []llm.StreamChunk
	requests   []llm.GenerateOptions
	dispatched bool
}

func (backend *reconstructionAdapter) Stream(
	_ context.Context,
	options llm.GenerateOptions,
) (llm.ChunkStream, error) {
	backend.mu.Lock()
	deferred, err := llm.CloneGenerateOptions(options)
	if err != nil {
		backend.mu.Unlock()
		return nil, err
	}
	if backend.dispatched {
		backend.mu.Unlock()
		return nil, errors.New("reconstruction adapter received an unexpected request")
	}
	backend.dispatched = true
	backend.requests = append(backend.requests, deferred)
	response := append([]llm.StreamChunk(nil), backend.response...)
	backend.mu.Unlock()
	return llm.NewSliceStream(response)
}

func (backend *reconstructionAdapter) snapshots() []llm.GenerateOptions {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	capturedCalls := make([]llm.GenerateOptions, len(backend.requests))
	for requestIndex, requestSnapshot := range backend.requests {
		capturedCalls[requestIndex], _ = llm.CloneGenerateOptions(requestSnapshot)
	}
	return capturedCalls
}

type reconstructionRequestObservation struct {
	Provider        string                        `json:"provider"`
	Model           string                        `json:"model"`
	ReasoningEffort llm.ReasoningEffortID         `json:"reasoningEffort,omitempty"`
	Temperature     *float64                      `json:"temperature,omitempty"`
	MaxTokens       *int                          `json:"maxTokens,omitempty"`
	Stop            []string                      `json:"stop,omitempty"`
	System          *string                       `json:"system,omitempty"`
	Tools           []string                      `json:"tools"`
	Messages        []agentLoopMessageObservation `json:"messages"`
	SessionID       string                        `json:"sessionId"`
}

type reconstructionGenerationObservation struct {
	Request             reconstructionRequestObservation `json:"request"`
	Rebuilt             reconstructionRequestObservation `json:"rebuilt"`
	HeaderReasons       []session.RequestHeaderReason    `json:"headerReasons"`
	RequestContextCount int                              `json:"requestContextCount"`
	ReplaceGeneration   uint64                           `json:"replaceGeneration"`
}

type reconstructionObservation struct {
	Generations []reconstructionGenerationObservation `json:"generations"`
}

func TestPinnedSourceRequestReconstructionMatchesGo(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	commandContext, cancelCommand := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCommand()
	sourceOutput, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "agent-loop-reconstruction.ts"),
		sourceRoot, filepath.Join(repositoryRoot, "contracts", "deepseek-harness", "manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}

	firstState, firstAdapter := newReconstructionHarness(t, []llm.StreamChunk{
		llm.BlockEndChunk{Index: 0, Block: llm.NewTextBlock("one")},
		llm.FinishChunk{Reason: llm.StopFinish{}},
	})
	firstHandle, err := firstState.loopRuntime.Create(
		context.Background(), firstState.providerScope, "reconstruction-first",
		agent.Options{Provider: "mock", Model: "model"}, session.Metadata{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if disposeErr := firstHandle.Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	}()
	if err := firstHandle.Subject.Followup(reconstructionUserMessage(t, "first", llm.UserMessageSource{})); err != nil {
		t.Fatal(err)
	}
	waitForReconstructionIdle(t, firstHandle.Subject)

	firstConversation := firstHandle.Subject.SessionValue()
	shadowed := firstConversation.Surface().Nodes
	if len(shadowed) != 2 {
		t.Fatalf("first surface nodes = %#v", shadowed)
	}
	summary := reconstructionUserMessage(t, "[summary of turn 1]", llm.PluginMessageSource{Plugin: "test-compact"})
	if _, err := session.AppendSurface(firstConversation, session.UserMessageAdded, summary, session.SurfaceIntent{
		Operation: session.SurfaceReplace(shadowed[0], shadowed[1]), SourceEventSeqs: &shadowed,
	}); err != nil {
		t.Fatal(err)
	}
	forkSeed := firstConversation.Events()
	firstRequests := firstAdapter.snapshots()
	if len(firstRequests) != 1 {
		t.Fatalf("first requests = %d", len(firstRequests))
	}
	firstObservation := observeReconstructionGeneration(t, firstRequests[0], firstConversation)

	resumedState, resumedAdapter := newReconstructionHarness(t, []llm.StreamChunk{
		llm.BlockEndChunk{Index: 0, Block: llm.NewTextBlock("two")},
		llm.FinishChunk{Reason: llm.StopFinish{}},
	})
	if _, err := resumedState.prompts.Section(context.Background(), resumedState.providerScope, systemprompt.PromptSection{
		Name: "extra", Order: 2, Text: systemprompt.StaticText("new guidance"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.OnRequest(resumedState.providerScope,
		func(requestContext context.Context, _ agent.RequestNotice, downstream agent.RequestNext) (llm.CallConfig, error) {
			proposed, resolveErr := downstream(requestContext)
			if resolveErr != nil {
				return llm.CallConfig{}, resolveErr
			}
			temperature := 0.5
			maxTokens := 99
			proposed.Temperature = &temperature
			proposed.MaxTokens = &maxTokens
			proposed.Stop = []string{"<END>"}
			return proposed, nil
		}); err != nil {
		t.Fatal(err)
	}
	resumedHandle, err := resumedState.agents.Create(context.Background(), resumedState.providerScope, agent.CreateOptions{
		SessionID: "reconstruction-resumed", Seed: forkSeed,
		AgentOptions: agent.Options{Provider: "mock", Model: "model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if disposeErr := resumedHandle.Dispose(context.Background()); disposeErr != nil {
			t.Error(disposeErr)
		}
	}()
	if err := resumedHandle.Subject.Followup(reconstructionUserMessage(t, "second", llm.UserMessageSource{})); err != nil {
		t.Fatal(err)
	}
	waitForReconstructionIdle(t, resumedHandle.Subject)
	resumedRequests := resumedAdapter.snapshots()
	if len(resumedRequests) != 1 {
		t.Fatalf("resumed requests = %d", len(resumedRequests))
	}
	resumedObservation := observeReconstructionGeneration(
		t, resumedRequests[0], resumedHandle.Subject.SessionValue(),
	)

	observation := reconstructionObservation{Generations: []reconstructionGenerationObservation{
		firstObservation, resumedObservation,
	}}
	for generationIndex, generation := range observation.Generations {
		if !reflect.DeepEqual(generation.Request, generation.Rebuilt) {
			t.Fatalf("generation %d request was not reconstructed: actual=%#v rebuilt=%#v",
				generationIndex, generation.Request, generation.Rebuilt)
		}
	}
	goOutput, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, goOutput, sourceOutput)
}

func newReconstructionHarness(
	t *testing.T,
	response []llm.StreamChunk,
) (*agentLoopContractState, *reconstructionAdapter) {
	t.Helper()
	backend := &reconstructionAdapter{response: append([]llm.StreamChunk(nil), response...)}
	state := &agentLoopContractState{engine: plugin.NewRuntime()}
	if _, err := state.engine.Load(context.Background(), &agentLoopContractProvider{state: state}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.models.RegisterAdapter(
		context.Background(), state.providerScope, []string{"mock"}, backend,
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.engine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return state, backend
}

func observeReconstructionGeneration(
	t *testing.T,
	requestSnapshot llm.GenerateOptions,
	conversation *session.Session,
) reconstructionGenerationObservation {
	t.Helper()
	events := conversation.Events()
	rebuilt := reconstructDispatchRequest(t, conversation.ID(), events)
	headerReasons := make([]session.RequestHeaderReason, 0, 2)
	requestContextCount := 0
	for _, event := range events {
		switch event.Type {
		case session.RequestHeaderEventName:
			var snapshot session.RequestHeaderSnapshot
			if err := json.Unmarshal(event.Data, &snapshot); err != nil {
				t.Fatal(err)
			}
			headerReasons = append(headerReasons, snapshot.Reason)
		case session.RequestContextEventName:
			requestContextCount++
		}
	}
	return reconstructionGenerationObservation{
		Request: projectReconstructionRequest(requestSnapshot), Rebuilt: rebuilt,
		HeaderReasons: headerReasons, RequestContextCount: requestContextCount,
		ReplaceGeneration: conversation.Surface().ReplaceGeneration,
	}
}

func reconstructDispatchRequest(
	t *testing.T,
	identifier session.SessionID,
	events []session.Event,
) reconstructionRequestObservation {
	t.Helper()
	chunkSequence := int64(-1)
	for eventIndex := len(events) - 1; eventIndex >= 0; eventIndex-- {
		if events[eventIndex].Type == session.AssistantChunkEventName {
			chunkSequence = events[eventIndex].Seq
			break
		}
	}
	if chunkSequence < 0 {
		t.Fatalf("Session %q has no dispatch chunk", identifier)
	}
	prefix := events[:chunkSequence]
	rebuiltSession, err := session.New("rebuild-"+identifier, session.CreateOptions{Seed: prefix})
	if err != nil {
		t.Fatal(err)
	}
	foldedHeader, found, err := rebuiltSession.RequestHeaderValue()
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("Session %q has no request header", identifier)
	}
	rebuiltMessages, err := rebuiltSession.DeriveMessages()
	if err != nil {
		t.Fatal(err)
	}
	return projectReconstructionRequest(llm.GenerateOptions{
		CallConfig: foldedHeader.Config, Messages: rebuiltMessages,
		System: foldedHeader.System, Tools: foldedHeader.Tools, SessionID: string(identifier),
	})
}

func projectReconstructionRequest(requestSnapshot llm.GenerateOptions) reconstructionRequestObservation {
	toolNames := make([]string, len(requestSnapshot.Tools))
	for toolIndex, schema := range requestSnapshot.Tools {
		toolNames[toolIndex] = schema.Name
	}
	return reconstructionRequestObservation{
		Provider: requestSnapshot.Provider, Model: requestSnapshot.Model,
		ReasoningEffort: requestSnapshot.ReasoningEffort,
		Temperature:     requestSnapshot.Temperature, MaxTokens: requestSnapshot.MaxTokens,
		Stop: append([]string(nil), requestSnapshot.Stop...), System: requestSnapshot.System,
		Tools: toolNames, Messages: projectAgentLoopMessages(requestSnapshot.Messages),
		SessionID: requestSnapshot.SessionID,
	}
}

func reconstructionUserMessage(t *testing.T, text string, origin llm.MessageSource) llm.UserMessage {
	t.Helper()
	messageValue, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock(text)}, Source: origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	return messageValue
}

func waitForReconstructionIdle(t *testing.T, subject agent.Agent) {
	t.Helper()
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := subject.WhenIdle(waitContext); err != nil {
		t.Fatal(err)
	}
}
