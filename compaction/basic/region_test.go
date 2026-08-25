package basic

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

type sessionStoreProbe struct {
	plugin.Base
	store session.LiveStore
}

func (*sessionStoreProbe) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "compaction-basic-session-store-probe",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[session.LiveStore](),
		},
	}
}

func (probe *sessionStoreProbe) Apply(context.Context) error {
	store, err := plugin.Require[session.LiveStore](probe)
	if err != nil {
		return err
	}
	probe.store = store
	return nil
}

func (*sessionStoreProbe) Dispose(context.Context) error { return nil }

func TestCompactRegionCommitsReplayableLLMCheckpoint(t *testing.T) {
	t.Parallel()
	runtimeValue := newRuntimeStub("small checkpoint", 1_000)
	meterValue := &meterStub{}
	storeValue := &liveStoreStub{}
	implementation := newBoundCompaction(
		t,
		Config{
			Auto: boolPointer(false),
		},
		runtimeValue,
		meterValue,
		storeValue,
		nil,
	)
	conversation := conversationFixture(t, 3, strings.Repeat("history ", 20))
	systemPrompt := "conversation system"
	{
		draft, err := session.NewEventDraft(session.RequestHeaderSet,
			session.RequestHeaderSnapshot{
				Header: session.EpochHeader{
					Config: llm.CallConfig{
						Provider: fixtureProvider,
						Model:    fixtureModel,
					},
					System: &systemPrompt,
					Tools: []llm.ToolSchema{
						{
							Name:        "lookup",
							Description: "lookup data",
							Parameters:  []byte(`{"type":"object"}`),
						},
					},
				},
				Reason: session.RequestHeaderResume,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	before := conversation.Surface()
	outcome, err := implementation.CompactRegion(
		context.Background(),
		before.Nodes[0],
		before.Nodes[3],
		compaction.AgentContext{
			Session:  conversation,
			Provider: "fallback-provider",
			Model:    "fallback-model",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(outcome.ShadowedSeqs, before.Nodes[:4]) ||
		outcome.ShadowedTokenCount != 400 || outcome.StartSeq >= outcome.SummarySeq ||
		outcome.SummarySeq >= outcome.EndSeq {
		t.Fatalf("outcome = %#v", outcome)
	}
	entries := conversation.Events()
	if entries[outcome.StartSeq].Type != compaction.StartEventName ||
		entries[outcome.SummarySeq].Type != compaction.SummaryEventName ||
		entries[outcome.SummarySeq+1].Type != session.UserMessageEventName ||
		entries[outcome.EndSeq].Type != compaction.EndEventName {
		t.Fatalf(
			"transaction events = %q, %q, %q, %q",
			entries[outcome.StartSeq].Type,
			entries[outcome.SummarySeq].Type,
			entries[outcome.SummarySeq+1].Type,
			entries[outcome.EndSeq].Type,
		)
	}
	summaryValue, err := compaction.DecodeSummary(entries[outcome.SummarySeq].Data)
	if err != nil {
		t.Fatal(err)
	}
	if summaryValue.CompactionID != outcome.CompactionID ||
		!summaryValue.LLMStreamCall || summaryValue.Provider != fixtureProvider ||
		summaryValue.Model != fixtureModel || summaryValue.MaxTokens == nil ||
		*summaryValue.MaxTokens != defaultMaxTokens || summaryValue.Usage == nil ||
		summaryValue.Usage.InputTokens != 40 || len(summaryValue.RawOutput) != 2 ||
		len(summaryValue.Summary) != 1 {
		t.Fatalf("summary event = %#v", summaryValue)
	}
	replacementEvent := entries[outcome.SummarySeq+1]
	wantSources := append(
		[]int64{
			outcome.StartSeq,
			outcome.SummarySeq,
		},
		outcome.ShadowedSeqs...,
	)
	if replacementEvent.SourceEventSeqs == nil ||
		!reflect.DeepEqual(*replacementEvent.SourceEventSeqs, wantSources) {
		t.Fatalf("replacement sources = %#v, want %#v", replacementEvent.SourceEventSeqs, wantSources)
	}
	derived, err := conversation.DeriveMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(derived) == 0 || !compaction.IsCheckpointSource(derived[0].SourceValue()) {
		t.Fatalf("checkpoint source = %#v", derived)
	}
	checkpointText := textFromBlocks(derived[0].ContentValue())
	if !strings.Contains(checkpointText, checkpointPreamble) ||
		!strings.Contains(checkpointText, summaryOpenTag+"small checkpoint"+summaryCloseTag) {
		t.Fatalf("checkpoint text = %q", checkpointText)
	}
	requests := runtimeValue.requestValues()
	if len(requests) != 1 || requests[0].Provider != fixtureProvider ||
		requests[0].Model != fixtureModel || requests[0].Purpose != llm.PurposeCompaction ||
		requests[0].System == nil || *requests[0].System != systemPrompt ||
		len(requests[0].Tools) != 1 || len(requests[0].Messages) != 5 {
		t.Fatalf("summary request = %#v", requests)
	}
	lastMessage := requests[0].Messages[len(requests[0].Messages)-1]
	if !strings.Contains(textFromBlocks(lastMessage.ContentValue()), "acting as a compaction engine") {
		t.Fatalf("summary instruction = %#v", lastMessage.ContentValue())
	}
	replayed, err := session.New(
		"compaction-replay",
		session.CreateOptions{
			Seed: entries,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	replayedMessages, err := replayed.DeriveMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(replayedMessages) != len(derived) ||
		textFromBlocks(replayedMessages[0].ContentValue()) != checkpointText ||
		!compaction.IsCheckpointSource(replayedMessages[0].SourceValue()) {
		t.Fatalf("replayed messages = %#v", replayedMessages)
	}
	state, err := compaction.InspectLog(entries)
	if err != nil || state.Attempt != nil {
		t.Fatalf("transaction state = %#v, error = %v", state, err)
	}
}

func TestCompactRegionRecordsSummaryFailureAndKeepsSurface(t *testing.T) {
	t.Parallel()
	runtimeValue := newRuntimeStub("truncated", 1_000)
	runtimeValue.chunks = []llm.StreamChunk{
		llm.TextDeltaChunk{
			Index: 0,
			Text:  "truncated",
		},
		llm.FinishChunk{
			Reason: llm.MaxTokensFinish{},
		},
	}
	implementation := newBoundCompaction(
		t,
		Config{
			Auto: boolPointer(false),
		},
		runtimeValue,
		&meterStub{},
		&liveStoreStub{},
		nil,
	)
	conversation := conversationFixture(t, 2, "history")
	before := conversation.Surface()
	_, err := implementation.CompactRegion(
		context.Background(),
		before.Nodes[0],
		before.Nodes[1],
		compaction.AgentContext{
			Session: conversation,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "truncated at the token cap") {
		t.Fatalf("CompactRegion error = %v", err)
	}
	after := conversation.Surface()
	if !reflect.DeepEqual(after.Nodes, before.Nodes) ||
		after.ReplaceGeneration != before.ReplaceGeneration {
		t.Fatalf("surface after failed summary = %#v, want %#v", after, before)
	}
	entries := conversation.Events()
	if entries[len(entries)-2].Type != compaction.StartEventName ||
		entries[len(entries)-1].Type != compaction.EndEventName {
		t.Fatalf("failed transaction tail = %#v", entries[len(entries)-2:])
	}
	ending, decodeErr := compaction.DecodeEnd(entries[len(entries)-1].Data)
	if decodeErr != nil || ending.Error == nil ||
		!strings.Contains(*ending.Error, "truncated at the token cap") {
		t.Fatalf("failed end = %#v, error = %v", ending, decodeErr)
	}
	state, inspectErr := compaction.InspectLog(entries)
	if inspectErr != nil || state.Attempt != nil {
		t.Fatalf("failed transaction state = %#v, error = %v", state, inspectErr)
	}
}

func TestCompactRegionRejectsConcurrentSurfaceChange(t *testing.T) {
	t.Parallel()
	runtimeValue := newRuntimeStub("checkpoint", 1_000)
	conversation := conversationFixture(t, 2, "history")
	runtimeValue.beforeCall = func() {
		injected, err := llm.NewUserMessage(llm.UserMessageInput{
			Content: []llm.ContentBlock{
				llm.NewTextBlock("injected while summarizing"),
			},
			Source: llm.UserMessageSource{},
		})
		if err != nil {
			panic(err)
		}
		{
			draft, err := session.NewSurfaceEventDraft(session.UserMessageAdded,
				injected,
				session.SurfaceIntent{
					Operation: session.SurfaceAppend(),
				})
			if err == nil {
				_, err = conversation.Commit(context.Background(), session.Batch(draft))
			}
			if err != nil {
				panic(err)
			}
		}
	}
	implementation := newBoundCompaction(
		t,
		Config{
			Auto: boolPointer(false),
		},
		runtimeValue,
		&meterStub{},
		&liveStoreStub{},
		nil,
	)
	before := conversation.Surface()
	_, err := implementation.CompactRegion(
		context.Background(),
		before.Nodes[0],
		before.Nodes[1],
		compaction.AgentContext{
			Session: conversation,
		},
	)
	var changed *surfaceChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("CompactRegion error = %T %v", err, err)
	}
	state, inspectErr := compaction.InspectLog(conversation.Events())
	if inspectErr != nil || state.Attempt != nil {
		t.Fatalf("changed transaction state = %#v, error = %v", state, inspectErr)
	}
}

func TestCompactNowUsesAgentMaintenanceAndFlushesClosedAttempt(t *testing.T) {
	t.Parallel()
	runtimeValue := newRuntimeStub("manual checkpoint", 1_000)
	storeValue := &liveStoreStub{}
	implementation := newBoundCompaction(
		t,
		Config{
			Auto: boolPointer(false),
		},
		runtimeValue,
		&meterStub{},
		storeValue,
		nil,
	)
	conversation := conversationFixture(t, 3, "manual history")
	{
		draft, err := session.NewEventDraft(session.TurnEnded,
			session.TurnEnd{
				Turn:   4,
				Reason: session.TurnCompleted{},
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	commandID := "command-1"
	outcome, err := implementation.CompactNow(
		context.Background(),
		&agentStub{
			identifier:   conversation.ID(),
			conversation: conversation,
			maintenance: &maintenanceStub{
				run: true,
			},
		},
		&commandID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if outcome == nil || outcome.SourceCommandID == nil ||
		*outcome.SourceCommandID != commandID || storeValue.flushCount() != 1 {
		t.Fatalf("manual outcome = %#v, flushes = %d", outcome, storeValue.flushCount())
	}
	startValue, err := compaction.DecodeStart(
		conversation.Events()[outcome.StartSeq].Data,
	)
	if err != nil || startValue.Turn != nil || startValue.SourceCommandID == nil ||
		*startValue.SourceCommandID != commandID {
		t.Fatalf("manual start = %#v, error = %v", startValue, err)
	}
}

func TestCompactNowAllowsChangesOutsideSelectedSpan(t *testing.T) {
	t.Parallel()
	conversation := conversationFixture(t, 3, "manual stable span")
	{
		draft, err := session.NewEventDraft(session.TurnEnded,
			session.TurnEnd{
				Turn:   4,
				Reason: session.TurnCompleted{},
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	runtimeValue := newRuntimeStub("manual checkpoint", 1_000)
	injectedSeq := int64(-1)
	runtimeValue.beforeCall = func() {
		injected, err := llm.NewUserMessage(llm.UserMessageInput{
			Content: []llm.ContentBlock{
				llm.NewTextBlock("outside the selected span"),
			},
			Source: llm.UserMessageSource{},
		})
		if err != nil {
			panic(err)
		}
		committed, err := session.Event{}, error(nil)
		{
			var committedEvent session.Event
			var writeErr error
			draft, draftErr := session.NewSurfaceEventDraft(session.UserMessageAdded,
				injected,
				session.SurfaceIntent{
					Operation: session.SurfaceAppend(),
				})
			writeErr = draftErr
			if draftErr == nil {
				receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
				writeErr = commitErr
				if commitErr == nil {
					committedEvent = receipt.Events[0]
				}
			}
			committed = committedEvent
			err = writeErr
		}

		if err != nil {
			panic(err)
		}
		injectedSeq = committed.Seq
	}
	implementation := newBoundCompaction(
		t,
		Config{
			Auto: boolPointer(false),
		},
		runtimeValue,
		&meterStub{},
		&liveStoreStub{},
		nil,
	)
	before := conversation.Surface()
	outcome, err := implementation.CompactNow(
		context.Background(),
		&agentStub{
			identifier:   conversation.ID(),
			conversation: conversation,
			maintenance: &maintenanceStub{
				run: true,
			},
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if outcome == nil {
		t.Fatal("manual compaction returned no result")
	}
	after := conversation.Surface()
	if len(after.Nodes) != 3 ||
		after.Nodes[1] != before.Nodes[len(before.Nodes)-1] ||
		after.Nodes[2] != injectedSeq {
		t.Fatalf("selected-span Surface = %#v, before %#v", after, before)
	}
}

func TestCompactNowPreservesCallerCancellationAndClassifiesAgentCancellation(t *testing.T) {
	t.Parallel()
	t.Run("caller cancellation", func(t *testing.T) {
		conversation := closedConversationFixture(t, 2, "caller cancellation")
		callerContext, cancelCaller := context.WithCancelCause(context.Background())
		callerCause := errors.New("caller stopped compaction")
		runtimeValue := newRuntimeStub("unused", 1_000)
		runtimeValue.beforeCall = func() {
			cancelCaller(callerCause)
		}
		storeValue := &liveStoreStub{}
		implementation := newBoundCompaction(
			t,
			Config{
				Auto: boolPointer(false),
			},
			runtimeValue,
			&meterStub{},
			storeValue,
			nil,
		)
		_, err := implementation.CompactNow(
			callerContext,
			&agentStub{
				identifier:   conversation.ID(),
				conversation: conversation,
				maintenance: &maintenanceStub{
					run: true,
				},
			},
			nil,
		)
		if err != callerCause {
			t.Fatalf("caller cancellation = %T %v, want exact cause", err, err)
		}
		if storeValue.flushCount() != 1 {
			t.Fatalf("caller cancellation flushes = %d, want 1", storeValue.flushCount())
		}
	})

	t.Run("agent cancellation", func(t *testing.T) {
		conversation := closedConversationFixture(t, 2, "agent cancellation")
		operationContext, cancelAgent := context.WithCancelCause(context.Background())
		runtimeValue := newRuntimeStub("unused", 1_000)
		runtimeValue.beforeCall = func() {
			cancelAgent(errors.New("agent cancelled maintenance"))
		}
		implementation := newBoundCompaction(
			t,
			Config{
				Auto: boolPointer(false),
			},
			runtimeValue,
			&meterStub{},
			&liveStoreStub{},
			nil,
		)
		_, err := implementation.CompactNow(
			context.Background(),
			&agentStub{
				identifier:   conversation.ID(),
				conversation: conversation,
				maintenance: &maintenanceStub{
					run:              true,
					operationContext: operationContext,
				},
			},
			nil,
		)
		assertManualErrorCode(t, err, compaction.ManualErrorCancelled)
	})
}

func TestCompactNowDoesNotReportSuccessWhenClosingMarkerFails(t *testing.T) {
	t.Parallel()
	timeSource := &closingFailureClock{}
	sessionPlugin, err := session.NewPlugin(session.MemoryStoreOptions{
		TimeSource:         timeSource,
		PostCommitFailures: ignoredPostCommitFailures{},
	})
	if err != nil {
		t.Fatal(err)
	}
	storeProbe := &sessionStoreProbe{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err = runtimeEngine.Start(
		context.Background(),
		sessionPlugin,
		storeProbe,
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if shutdownErr := runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})
	store := storeProbe.store
	identifier := session.SessionID("closing-marker-failure")
	conversation, err := store.Prepare(
		&identifier,
		session.CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	populateConversationFixture(t, conversation, 2, "closing marker")
	{
		draft, err := session.NewEventDraft(session.TurnEnded,
			session.TurnEnd{
				Turn:   3,
				Reason: session.TurnCompleted{},
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	runtimeValue := newRuntimeStub("small checkpoint", 1_000)
	runtimeValue.beforeCall = timeSource.arm
	storeValue := &liveStoreStub{}
	implementation := newBoundCompaction(
		t,
		Config{
			Auto: boolPointer(false),
		},
		runtimeValue,
		&meterStub{},
		storeValue,
		nil,
	)
	_, err = implementation.CompactNow(
		context.Background(),
		&agentStub{
			identifier:   conversation.ID(),
			conversation: conversation,
			maintenance: &maintenanceStub{
				run: true,
			},
		},
		nil,
	)
	assertManualErrorCode(t, err, compaction.ManualErrorCommit)
	if storeValue.flushCount() != 0 {
		t.Fatalf("unclosed transaction flushes = %d, want 0", storeValue.flushCount())
	}
	state, inspectErr := compaction.InspectLog(conversation.Events())
	if inspectErr != nil || state.Attempt == nil {
		t.Fatalf("closing failure state = %#v, error = %v", state, inspectErr)
	}
}

func TestCompactNowClassifiesBusyAndPersistenceFailure(t *testing.T) {
	t.Parallel()
	runtimeValue := newRuntimeStub("manual checkpoint", 1_000)
	storeValue := &liveStoreStub{}
	implementation := newBoundCompaction(
		t,
		Config{
			Auto: boolPointer(false),
		},
		runtimeValue,
		&meterStub{},
		storeValue,
		nil,
	)
	busyConversation := conversationFixture(t, 1, "busy")
	_, err := implementation.CompactNow(
		context.Background(),
		&agentStub{
			identifier:   busyConversation.ID(),
			conversation: busyConversation,
			maintenance: &maintenanceStub{
				returnErr: errors.New("already active"),
			},
		},
		nil,
	)
	assertManualErrorCode(t, err, compaction.ManualErrorBusy)
	if strings.Contains(err.Error(), "already active") {
		t.Fatalf("busy public message leaked cause = %v", err)
	}

	persistenceConversation := conversationFixture(t, 2, "persistence")
	{
		draft, err := session.NewEventDraft(session.TurnEnded,
			session.TurnEnd{
				Turn:   3,
				Reason: session.TurnCompleted{},
			})
		if err == nil {
			_, err = persistenceConversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	storeValue.flushError = errors.New("disk unavailable")
	_, err = implementation.CompactNow(
		context.Background(),
		&agentStub{
			identifier:   persistenceConversation.ID(),
			conversation: persistenceConversation,
			maintenance: &maintenanceStub{
				run: true,
			},
		},
		nil,
	)
	assertManualErrorCode(t, err, compaction.ManualErrorPersistence)
	state, inspectErr := compaction.InspectLog(persistenceConversation.Events())
	if inspectErr != nil || state.Attempt != nil {
		t.Fatalf("persistence transaction state = %#v, error = %v", state, inspectErr)
	}
}

func assertManualErrorCode(
	testingContext *testing.T,
	problem error,
	want compaction.ManualErrorCode,
) {
	testingContext.Helper()
	var manualProblem *compaction.ManualError
	if !errors.As(problem, &manualProblem) || manualProblem.Code != want {
		testingContext.Fatalf("manual error = %T %v, want code %q", problem, problem, want)
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func closedConversationFixture(
	testingContext *testing.T,
	closedTurns int,
	text string,
) session.Context {
	testingContext.Helper()
	conversation := conversationFixture(testingContext, closedTurns, text)
	{
		draft, err := session.NewEventDraft(session.TurnEnded,
			session.TurnEnd{
				Turn:   int64(closedTurns + 1),
				Reason: session.TurnCompleted{},
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
	return conversation
}

type closingFailureClock struct {
	mutex sync.Mutex
	armed bool
	calls int
}

func (clock *closingFailureClock) CurrentTime() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	if !clock.armed {
		return time.UnixMilli(1)
	}
	clock.calls++
	if clock.calls >= 3 {
		return time.UnixMilli(1 << 53)
	}
	return time.UnixMilli(int64(clock.calls + 1))
}

func (clock *closingFailureClock) arm() {
	clock.mutex.Lock()
	clock.armed = true
	clock.calls = 0
	clock.mutex.Unlock()
}

type ignoredPostCommitFailures struct{}

func (ignoredPostCommitFailures) ReportPostCommitFailure(session.PostCommitFailure) {}
