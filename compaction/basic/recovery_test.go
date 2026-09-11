package basic

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func recoveryFixture(t *testing.T) (*session.Log, *Recovery, agent.RequestFailure) {
	t.Helper()
	history, _ := transactionSession(t)
	flowRoute(t, history)
	settings, err := ResolveConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewRecovery(history.Snapshot().Barrier.SessionID, settings)
	if err != nil {
		t.Fatal(err)
	}
	return history, owner, agent.RequestFailure{
		AgentID:  history.Snapshot().Barrier.SessionID,
		Turn:     1,
		Step:     1,
		Provider: "summary",
		Failure: llm.LlmFailure{
			Code:    llm.ContextWindowExceededCode,
			Message: "context exceeded",
		},
	}
}

func prunedOverflow(t *testing.T, history *session.Log, changed bool, failure error) *Compaction {
	t.Helper()
	execution := flowOwner(t)
	snapshot := history.Snapshot()
	if err := execution.Begin(Invocation{
		Mode:        Overflow,
		Snapshot:    snapshot,
		Measurement: flowPrices(snapshot, 100),
		Prune:       true,
	}); err != nil {
		t.Fatal(err)
	}
	if changed {
		entry, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("shorter")},
			Source:  agentmessage.UserMessageSource{},
		})
		if err != nil {
			t.Fatal(err)
		}
		sources := []int64{snapshot.Surface.Nodes[0]}
		draft, err := session.NewSurfaceEventDraft(session.UserMessageAdded, entry, session.SurfaceIntent{
			Operation:       session.SurfaceReplace(sources[0], sources[0]),
			SourceEventSeqs: &sources,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := history.Commit(context.Background(), session.Batch(draft)); err != nil {
			t.Fatal(err)
		}
	}
	if err := execution.Pruned(history.Snapshot(), failure); err != nil {
		t.Fatal(err)
	}
	return execution
}

func TestRecoveryConsumesBudgetOnlyForConfirmedProgress(t *testing.T) {
	history, owner, input := recoveryFixture(t)
	failure := errors.New("compaction failed after pruning")
	for _, changed := range []bool{false, true} {
		accepted, err := owner.Prepare(input, history.Snapshot())
		if err != nil || !accepted {
			t.Fatalf("admission = %v, %v", accepted, err)
		}
		if _, err := owner.Prepare(input, history.Snapshot()); err == nil {
			t.Fatal("overlapping recovery accepted")
		}
		decision, err := owner.Complete(prunedOverflow(t, history, changed, failure), nil)
		if err != nil || decision.Retry != changed || !errors.Is(decision.Diagnostic, failure) {
			t.Fatalf("decision = %#v, %v", decision, err)
		}
	}
	if accepted, err := owner.Prepare(input, history.Snapshot()); err != nil || accepted {
		t.Fatalf("exhausted budget = %v, %v", accepted, err)
	}
	ended, err := session.NewEventDraft(session.TurnEnded, session.TurnEnd{
		Turn:   1,
		Reason: session.TurnCompleted{},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := session.NewEventDraft(session.TurnStarted, session.TurnStart{
		Turn: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(context.Background(), session.Batch(ended, started)); err != nil {
		t.Fatal(err)
	}
	input.Turn = 2
	if accepted, err := owner.Prepare(input, history.Snapshot()); err != nil || !accepted {
		t.Fatalf("new Turn budget = %v, %v", accepted, err)
	}
}

func TestRecoveryCancellationOverridesProgressWithoutConsumingBudget(t *testing.T) {
	history, owner, input := recoveryFixture(t)
	if accepted, err := owner.Prepare(input, history.Snapshot()); err != nil || !accepted {
		t.Fatal(err)
	}
	execution := prunedOverflow(t, history, true, errors.New("summary failed"))
	canceled := errors.New("canceled")
	decision, err := owner.Complete(execution, canceled)
	if !errors.Is(err, canceled) || decision.Retry {
		t.Fatalf("canceled decision = %#v, %v", decision, err)
	}
	if accepted, err := owner.Prepare(input, history.Snapshot()); err != nil || !accepted {
		t.Fatalf("cancellation consumed budget: %v, %v", accepted, err)
	}
	if err := owner.Reject(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryRequiresCompletedInvocationAndCurrentIdentity(t *testing.T) {
	history, owner, input := recoveryFixture(t)
	wrong := input
	wrong.AgentID = "other"
	if _, err := owner.Prepare(wrong, history.Snapshot()); err == nil {
		t.Fatal("wrong Agent accepted")
	}
	if accepted, err := owner.Prepare(input, history.Snapshot()); err != nil || !accepted {
		t.Fatal(err)
	}
	unfinished := flowOwner(t)
	if _, err := owner.Complete(unfinished, nil); err == nil {
		t.Fatal("unfinished compaction accepted")
	}
	if err := owner.Reject(); err != nil {
		t.Fatal("invalid completion lost pending recovery", err)
	}
	wrong = input
	wrong.Failure.Code = "OTHER"
	if accepted, err := owner.Prepare(wrong, history.Snapshot()); err != nil || accepted {
		t.Fatalf("non-overflow = %v, %v", accepted, err)
	}
}

func TestRecoveryResetsAfterCommittedAssistantMessageAndRejectsOlderSnapshot(t *testing.T) {
	history, owner, input := recoveryFixture(t)
	if accepted, err := owner.Prepare(input, history.Snapshot()); err != nil || !accepted {
		t.Fatal(err)
	}
	if _, err := owner.Complete(prunedOverflow(t, history, true, errors.New("summary failed")), nil); err != nil {
		t.Fatal(err)
	}
	earlier := history.Snapshot()
	if accepted, err := owner.Prepare(input, earlier); err != nil || accepted {
		t.Fatal("budget not exhausted", err)
	}
	message, err := agentmessage.NewAssistantMessage(agentmessage.AssistantMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("response")},
		Source: agentmessage.ModelMessageSource{
			Provider: "summary",
			Model:    "checkpoint",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := session.NewSurfaceEventDraft(session.AssistantMessaged, session.AssistantMessage{
		Turn:    1,
		Step:    1,
		Message: message,
	}, session.SurfaceIntent{
		Operation: session.SurfaceAppend(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(context.Background(), session.Batch(draft)); err != nil {
		t.Fatal(err)
	}
	input.Step = 2
	if accepted, err := owner.Prepare(input, history.Snapshot()); err != nil || !accepted {
		t.Fatalf("response did not reset budget: %v %v", accepted, err)
	}
	if err := owner.Reject(); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Prepare(input, earlier); err == nil {
		t.Fatal("older response boundary accepted")
	}
}
