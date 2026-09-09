package agentloop_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/agentloop/internal/modelrequest"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

// executionEnvironment supplies deterministic asynchronous capabilities. Tests
// release results, never choose the Agent's next business operation.
type executionEnvironment struct {
	history       *session.Log
	queued        []func()
	trace         []string
	chunks        []llm.StreamChunk
	generated     int
	closed        int
	reject        bool
	modelFailure  error
	closeFailure  error
	failure       error
	finished      bool
	flush         agentloop.FlushReceiver
	barrier       session.WriteBarrier
	commitFailure string
	rejectCommit  bool
	rejectAt      int
	submitted     int
	commits       map[string]int
}

func (fixture *executionEnvironment) services() agentloop.Services {
	return agentloop.Services{
		Conversation: executionSession{fixture: fixture},
		Prompts:      fixture,
		Middleware:   fixture,
		Models:       executionModel{fixture: fixture},
		Observer:     fixture,
	}
}

func (fixture *executionEnvironment) drain(t *testing.T) {
	t.Helper()
	for count := 0; len(fixture.queued) > 0; count++ {
		if count == 100 {
			t.Fatal("business failed to reach an external wait or completion")
		}
		next := fixture.queued[0]
		fixture.queued = fixture.queued[1:]
		next()
	}
}

func (fixture *executionEnvironment) ExecutionFailed(failure error) {
	fixture.failure = errors.Join(fixture.failure, failure)
}

func (fixture *executionEnvironment) ExecutionFinished() { fixture.finished = true }

func (fixture *executionEnvironment) Assemble(receiver agentloop.PromptReceiver) error {
	fixture.trace = append(fixture.trace, "prompt")
	fixture.queued = append(fixture.queued, func() {
		receiver.PromptAssembled(fixture.services(), systemprompt.PromptAssembly{}, nil)
	})
	return nil
}

func (fixture *executionEnvironment) PreStep(input agentloop.PreStep, receiver agentloop.PreStepReceiver) error {
	fixture.trace = append(fixture.trace, "pre-step")
	decision := input.Default
	if fixture.reject {
		decision = agent.PreStepDecision{Kind: agent.PreStepReject}
	}
	fixture.queued = append(fixture.queued, func() {
		receiver.PreStepResolved(fixture.services(), agent.PreStepResolved{Decision: decision})
	})
	return nil
}

func (fixture *executionEnvironment) TurnStopping(_ agent.TurnStoppingInput, receiver agentloop.StoppingReceiver) error {
	fixture.trace = append(fixture.trace, "turn-stopping")
	fixture.queued = append(fixture.queued, func() {
		receiver.TurnStoppingCompleted(fixture.services(), agent.TurnStoppingCompleted{})
	})
	return nil
}

func (fixture *executionEnvironment) RequestError(_ agent.RequestFailure, receiver agentloop.RecoveryReceiver) error {
	fixture.trace = append(fixture.trace, "request-error")
	fixture.queued = append(fixture.queued, func() {
		receiver.RequestErrorResolved(fixture.services(), agent.RequestErrorResolved{})
	})
	return nil
}

type executionSession struct{ fixture *executionEnvironment }

func (endpoint executionSession) Commit(plan session.WritePlan, receiver agentloop.CommitReceiver) error {
	fixture := endpoint.fixture
	fixture.submitted++
	reject := fixture.submitted == fixture.rejectAt
	fixture.queued = append(fixture.queued, func() {
		if fixture.commits == nil {
			fixture.commits = make(map[string]int)
		}
		if fixture.rejectCommit || reject {
			fixture.rejectCommit = false
			receiver.SessionCommitted(fixture.services(), session.WriteResult{}, errors.New("commit rejected"))
			return
		}
		result, err := fixture.history.Commit(context.Background(), plan)
		boundary := ""
		if len(result.Events) > 0 {
			boundary = result.Events[0].Type
		}
		fixture.commits[boundary]++
		for _, event := range result.Events {
			fixture.trace = append(fixture.trace, event.Type)
		}
		if boundary != "" && boundary == fixture.commitFailure {
			fixture.commitFailure = ""
			err = errors.New("commit acknowledgement lost")
		}
		receiver.SessionCommitted(fixture.services(), result, err)
	})
	return nil
}

func TestBusinessPreparationRecoversUncertainWritesBeforeStopping(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		lostReply string
		rejectAt  int
		steps     int
		reads     int
		pending   bool
	}{
		{
			name:      "claim-reply-lost",
			lostReply: agent.InboxSplicedEventName,
			reads:     1,
		},
		{
			name:     "claim-rejected",
			rejectAt: 2,
			reads:    1,
			pending:  true,
		},
		{
			name:      "step-start-reply-lost",
			lostReply: session.StepStartEventName,
			steps:     1,
			reads:     2,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			subject, history := readyAgent(t)
			proposal, err := subject.Followup(inputMessage(t, "hello"))
			if err != nil {
				t.Fatal(err)
			}
			confirmInput(t, subject, history, proposal)
			fixture := &executionEnvironment{
				history:       history,
				commitFailure: scenario.lostReply,
				rejectAt:      scenario.rejectAt,
			}
			err = subject.Run(fixture.services())
			if err != nil {
				t.Fatal(err)
			}
			fixture.drain(t)
			if fixture.flush == nil || fixture.generated != 0 {
				t.Fatalf("uncertain preparation continued model work or did not settle: %v, %v", fixture.trace, fixture.failure)
			}
			counts := make(map[string]int)
			for _, entry := range history.Events() {
				counts[entry.Type]++
			}
			if counts[session.StepStartEventName] != scenario.steps || counts[session.StepEndEventName] != scenario.steps || counts[session.TurnEndEventName] != 1 {
				t.Fatalf("incorrect recovered boundaries: %v", counts)
			}
			reads := 0
			for _, operation := range fixture.trace {
				if operation == "session-read" {
					reads++
				}
			}
			if reads != scenario.reads || subject.InboxValue().HasPending() != scenario.pending {
				t.Fatalf("recovery: reads=%d pending=%v", reads, subject.InboxValue().HasPending())
			}
		})
	}
}

func (endpoint executionSession) Read(receiver agentloop.SnapshotReceiver) error {
	fixture := endpoint.fixture
	fixture.trace = append(fixture.trace, "session-read")
	fixture.queued = append(fixture.queued, func() {
		receiver.SessionRead(fixture.services(), fixture.history.Snapshot(), nil)
	})
	return nil
}

func (endpoint executionSession) Flush(barrier session.WriteBarrier, receiver agentloop.FlushReceiver) error {
	endpoint.fixture.trace = append(endpoint.fixture.trace, "flush")
	endpoint.fixture.flush = receiver
	endpoint.fixture.barrier = barrier
	return nil
}

type executionModel struct{ fixture *executionEnvironment }

func (endpoint executionModel) Generate(_ llm.Input, receiver agentloop.GenerationReceiver) error {
	fixture := endpoint.fixture
	fixture.generated++
	fixture.trace = append(fixture.trace, "generate")
	if fixture.modelFailure != nil {
		return fixture.modelFailure
	}
	fixture.queued = append(fixture.queued, func() {
		receiver.GenerationStarted(fixture.services(), modelrequest.GenerationStarted{
			Config: llm.CallConfig{
				Provider: "test",
				Model:    "test",
			},
		})
	})
	return nil
}

func (endpoint executionModel) Read(receiver agentloop.ChunkReceiver) error {
	fixture := endpoint.fixture
	fixture.queued = append(fixture.queued, func() {
		result := modelrequest.ChunkRead{}
		if len(fixture.chunks) > 0 {
			result.Chunk = fixture.chunks[0]
			result.Found = true
			fixture.chunks = fixture.chunks[1:]
		}
		receiver.ChunkRead(fixture.services(), result, nil)
	})
	return nil
}

func (endpoint executionModel) Close(receiver agentloop.StreamReceiver) error {
	fixture := endpoint.fixture
	fixture.closed++
	fixture.trace = append(fixture.trace, "stream-close")
	failure := fixture.closeFailure
	fixture.closeFailure = nil
	fixture.queued = append(fixture.queued, func() {
		receiver.StreamClosed(fixture.services(), failure)
	})
	return nil
}

func TestBusinessExecutionRetriesOnlyUnconfirmedModelClose(t *testing.T) {
	subject, history := readyAgent(t)
	proposal, err := subject.Followup(inputMessage(t, "hello"))
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	providerFailure := errors.New("provider stopped responding")
	fixture := &executionEnvironment{
		history:      history,
		modelFailure: providerFailure,
		closeFailure: errors.New("close acknowledgement lost"),
	}
	err = subject.Run(fixture.services())
	if err != nil {
		t.Fatal(err)
	}
	fixture.drain(t)
	if fixture.flush != nil || fixture.finished || fixture.closed != 1 {
		t.Fatalf("unconfirmed close released work: %v", fixture.trace)
	}
	for _, entry := range history.Events() {
		if entry.Type == session.StepEndEventName || entry.Type == session.TurnEndEventName {
			t.Fatal("unconfirmed close persisted execution completion")
		}
	}
	err = subject.Run(fixture.services())
	if err != nil {
		t.Fatal(err)
	}
	fixture.drain(t)
	if fixture.flush == nil || fixture.generated != 1 || fixture.closed != 2 {
		t.Fatalf("retry repeated generation or failed to settle: %v, %v", fixture.trace, fixture.failure)
	}
	fixture.flush.SessionFlushed(fixture.services(), fixture.barrier, nil)
	if !fixture.finished || subject.StatusValue() != agent.StatusIdle || !errors.Is(fixture.failure, providerFailure) {
		t.Fatalf("retry lost the original outcome: %v", fixture.failure)
	}
}

func TestBusinessExecutionOwnsEntireTextTurnWithoutActor(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		reject       bool
		modelFailure error
	}{
		{name: "text"},
		{
			name:   "pre-step-rejection",
			reject: true,
		},
		{
			name:         "model-submission-failure",
			modelFailure: errors.New("model unavailable"),
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			subject, history := readyAgent(t)
			proposal, err := subject.Followup(inputMessage(t, "hello"))
			if err != nil {
				t.Fatal(err)
			}
			confirmInput(t, subject, history, proposal)
			fixture := &executionEnvironment{
				history:      history,
				reject:       scenario.reject,
				modelFailure: scenario.modelFailure,
				chunks: []llm.StreamChunk{
					llm.BlockStartChunk{BlockType: "text"},
					llm.TextDeltaChunk{Text: "response"},
					llm.BlockEndChunk{Block: agentmessage.NewTextBlock("response")},
					llm.FinishChunk{Reason: llm.StopFinish{}},
				},
			}
			err = subject.Run(fixture.services())
			if err != nil {
				t.Fatal(err)
			}
			fixture.drain(t)
			if fixture.flush == nil {
				t.Fatalf("business did not reach Turn flush: trace=%v failure=%v", fixture.trace, fixture.failure)
			}
			if fixture.finished || subject.StatusValue() != agent.StatusRunning {
				t.Fatal("execution finished before Session flush")
			}
			fixture.flush.SessionFlushed(fixture.services(), fixture.barrier, nil)
			fixture.drain(t)
			if !fixture.finished || subject.StatusValue() != agent.StatusIdle {
				t.Fatalf("business did not become idle: %v", fixture.failure)
			}
			if !errors.Is(fixture.failure, scenario.modelFailure) {
				t.Fatalf("failure=%v, expected=%v", fixture.failure, scenario.modelFailure)
			}
			if scenario.reject {
				if fixture.generated != 0 || slices.Contains(fixture.trace, "prompt") || slices.Contains(fixture.trace, session.StepStartEventName) {
					t.Fatalf("rejection continued execution: %v", fixture.trace)
				}
				return
			}
			if fixture.generated != 1 || fixture.closed != 1 {
				t.Fatalf("generation=%d, close=%d", fixture.generated, fixture.closed)
			}
			if slices.Index(fixture.trace, "pre-step") >= slices.Index(fixture.trace, "prompt") {
				t.Fatalf("context prepared before admission: %v", fixture.trace)
			}
			if !slices.Contains(fixture.trace, session.StepEndEventName) || !slices.Contains(fixture.trace, session.TurnEndEventName) {
				t.Fatalf("missing execution boundaries: %v", fixture.trace)
			}
			if scenario.modelFailure == nil && !slices.Contains(fixture.trace, session.AssistantMessageEventName) {
				t.Fatalf("missing model reply: %v", fixture.trace)
			}
		})
	}
}

func TestBusinessExecutionRetriesPersistenceWithoutRepeatingWork(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		boundary string
		reject   bool
		flush    bool
	}{
		{
			name:     "turn-start-reply-lost",
			boundary: session.TurnStartEventName,
		},
		{
			name:   "turn-start-rejected",
			reject: true,
		},
		{
			name:     "step-end-reply-lost",
			boundary: session.StepEndEventName,
		},
		{
			name:     "turn-end-reply-lost",
			boundary: session.TurnEndEventName,
		},
		{
			name:  "flush-failed",
			flush: true,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			subject, history := readyAgent(t)
			proposal, err := subject.Followup(inputMessage(t, "hello"))
			if err != nil {
				t.Fatal(err)
			}
			confirmInput(t, subject, history, proposal)
			providerFailure := errors.New("provider unavailable")
			fixture := &executionEnvironment{
				history:       history,
				commitFailure: scenario.boundary,
				rejectCommit:  scenario.reject,
				modelFailure:  providerFailure,
			}
			err = subject.Run(fixture.services())
			if err != nil {
				t.Fatal(err)
			}
			fixture.drain(t)
			if scenario.flush {
				if fixture.flush == nil {
					t.Fatalf("flush not requested: %v", fixture.failure)
				}
				fixture.flush.SessionFlushed(fixture.services(), session.WriteBarrier{}, errors.New("flush failed"))
				fixture.flush = nil
			}
			if fixture.failure == nil || fixture.finished || subject.StatusValue() != agent.StatusRunning {
				t.Fatalf("failed persistence released execution: finished=%v error=%v", fixture.finished, fixture.failure)
			}
			err = subject.Run(fixture.services())
			if err != nil {
				t.Fatal(err)
			}
			fixture.drain(t)
			if fixture.flush == nil {
				t.Fatalf("retry failed to reach flush: %v", fixture.failure)
			}
			// An unrelated wake while flush is outstanding must not repeat work.
			err = subject.Run(fixture.services())
			if err != nil {
				t.Fatal(err)
			}
			fixture.drain(t)
			fixture.flush.SessionFlushed(fixture.services(), fixture.barrier, nil)
			fixture.drain(t)
			if !fixture.finished || subject.StatusValue() != agent.StatusIdle {
				t.Fatalf("retry did not settle execution: %v", fixture.failure)
			}
			if !errors.Is(fixture.failure, providerFailure) {
				t.Fatalf("execution error lost across persistence retry: %v", fixture.failure)
			}
			if fixture.generated != 1 || fixture.closed != 1 {
				t.Fatalf("repeated model work: generation=%d close=%d", fixture.generated, fixture.closed)
			}
			counts := make(map[string]int)
			for _, entry := range history.Events() {
				counts[entry.Type]++
			}
			for _, boundary := range []string{session.TurnStartEventName, session.StepStartEventName, session.StepEndEventName, session.TurnEndEventName} {
				if counts[boundary] != 1 {
					t.Fatalf("%s persisted %d times", boundary, counts[boundary])
				}
			}
			wantFlushes := 1
			if scenario.flush {
				wantFlushes = 2
			}
			flushes := 0
			for _, operation := range fixture.trace {
				if operation == "flush" {
					flushes++
				}
			}
			if flushes != wantFlushes {
				t.Fatalf("flush requests=%d, want %d", flushes, wantFlushes)
			}
			if scenario.flush && fixture.commits[session.TurnEndEventName] != 1 {
				t.Fatal("flush retry resubmitted Turn end")
			}
		})
	}
}

func TestBusinessExecutionCancellationBeforeTurnPreservesInbox(t *testing.T) {
	subject, history := readyAgent(t)
	proposal, err := subject.Followup(inputMessage(t, "keep for restart"))
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	_, err = subject.Cancel(agent.UserCancel{}, agent.CancelOptions{
		KeepInbox: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &executionEnvironment{
		history: history,
	}
	err = subject.Run(fixture.services())
	if err != nil {
		t.Fatal(err)
	}
	fixture.drain(t)
	if !fixture.finished || subject.StatusValue() != agent.StatusIdle {
		t.Fatalf("cancelled admission stayed running: %v", fixture.failure)
	}
	if len(fixture.trace) != 0 {
		t.Fatalf("cancelled admission opened work: %v", fixture.trace)
	}
	if !subject.InboxValue().HasPending() {
		t.Fatal("cancellation lost preserved input")
	}
}

func TestBusinessExecutionResumesInputAfterMaintenance(t *testing.T) {
	subject, history := readyAgent(t)
	err := subject.BeginMaintenance()
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := subject.Followup(inputMessage(t, "after maintenance"))
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	fixture := &executionEnvironment{
		history: history,
		reject:  true,
	}
	err = subject.Run(fixture.services())
	if err != nil {
		t.Fatal(err)
	}
	fixture.drain(t)
	if len(fixture.trace) != 0 {
		t.Fatal("input ran before maintenance completion")
	}
	err = subject.MaintenanceCompleted(fixture.services())
	if err != nil {
		t.Fatal(err)
	}
	fixture.drain(t)
	if fixture.flush == nil {
		t.Fatalf("accepted input was not resumed: %v", fixture.failure)
	}
	fixture.flush.SessionFlushed(fixture.services(), fixture.barrier, nil)
	if !fixture.finished || subject.StatusValue() != agent.StatusIdle {
		t.Fatalf("resumed execution did not finish: %v", fixture.failure)
	}
}
