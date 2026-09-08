package agentloop_test

import (
	"context"
	"errors"
	"github.com/gorenx/goren/session"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/llm/modelconfig"
)

func TestLifecycleRejectsReinitializationAndRestoration(t *testing.T) {
	subject, history := readyAgent(t)
	if err := subject.Initialize(modelconfig.Resolved{}, 2); !errors.Is(err, agentloop.ErrState) {
		t.Fatalf("second initialization = %v", err)
	}
	if _, err := subject.Restore(history.Header(), history.Snapshot()); !errors.Is(err, agentloop.ErrState) {
		t.Fatalf("second restoration = %v", err)
	}
	if err := subject.Release(); !errors.Is(err, agentloop.ErrState) {
		t.Fatalf("release before close = %v", err)
	}
	if _, err := subject.Followup(inputMessage(t, "still available")); err != nil {
		t.Fatal("rejected transitions damaged admission", err)
	}
}

func TestLifecycleInitializationFailureCanRetryWithoutBecomingReady(t *testing.T) {
	_, history := readyAgent(t)
	subject := &agentloop.ReactLoopAgent{}
	if err := subject.Initialize(modelconfig.Resolved{}, 1); !errors.Is(err, agentloop.ErrState) {
		t.Fatalf("initialize before restore = %v", err)
	}
	if _, err := subject.Restore(history.Header(), history.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := subject.Initialize(modelconfig.Resolved{}, 0); err == nil {
		t.Fatal("unpublished tools initialized Agent")
	}
	if _, err := subject.Inject(inputMessage(t, "not ready")); !errors.Is(err, agentloop.ErrState) {
		t.Fatalf("input after failed initialization = %v", err)
	}
	if err := subject.Initialize(modelconfig.Resolved{}, 1); err != nil {
		t.Fatal("valid initialization retry", err)
	}
}

func TestLifecycleReleaseIsTerminalAndRequiresClosedWork(t *testing.T) {
	subject, history := readyAgent(t)
	input := inputMessage(t, "pending")
	proposal, err := subject.Inject(input)
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	if err := subject.PrepareClose(false); !errors.Is(err, agentloop.ErrPending) {
		t.Fatal("close accepted unfinished input", err)
	}
	if err := subject.Release(); !errors.Is(err, agentloop.ErrPending) {
		t.Fatal("release discarded input", err)
	}
	if _, err := subject.Followup(input); !errors.Is(err, agent.ErrClosing) {
		t.Fatal("closing accepted input", err)
	}
	if err := subject.Initialize(modelconfig.Resolved{}, 1); !errors.Is(err, agentloop.ErrState) {
		t.Fatal("closing initialized", err)
	}
	edit, err := subject.Remove(input.StableID())
	if err != nil {
		t.Fatal(err)
	}
	// Apply the same durable-edit path used by the runtime, without waking execution.
	confirmInput(t, subject, history, agent.Admission{
		Edit: edit,
	})
	if err := subject.Release(); err != nil {
		t.Fatal(err)
	}
	if err := subject.Release(); err != nil {
		t.Fatal("release retry", err)
	}
	if _, err := subject.Restore(history.Header(), history.Snapshot()); !errors.Is(err, agentloop.ErrState) {
		t.Fatal("released Agent restored", err)
	}
	if err := subject.Initialize(modelconfig.Resolved{}, 1); !errors.Is(err, agentloop.ErrState) {
		t.Fatal("released Agent initialized", err)
	}
	if subject.InboxValue() != nil {
		t.Fatal("released Agent retained projection")
	}
}

func TestTurnStateRejectsUnrequestedAndRepeatedConfirmation(t *testing.T) {
	subject, history := readyAgent(t)
	if err := subject.ConfirmTurn(session.WriteResult{}); !errors.Is(err, agentloop.ErrState) {
		t.Fatal("unrequested Turn confirmation", err)
	}
	proposal, err := subject.Followup(inputMessage(t, "start"))
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	draft, err := subject.PrepareTurn()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := subject.PreparePosition(); err == nil {
		t.Fatal("uncommitted Turn admitted Step")
	}
	write, err := history.Commit(context.Background(), session.Batch(draft))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := subject.Cancel(agent.UserCancel{}, agent.CancelOptions{KeepInbox: true}); err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmTurn(write); err != nil {
		t.Fatal("cancellation erased committed start", err)
	}
	if err := subject.ConfirmTurn(write); !errors.Is(err, agentloop.ErrState) {
		t.Fatal("repeated confirmation", err)
	}
	if _, err := subject.FinishActivity(); err == nil {
		t.Fatal("committed canceled Turn was not retained for settlement")
	}
}
