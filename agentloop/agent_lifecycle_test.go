package agentloop_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/session"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
)

func TestLifecycleRejectsReinitializationAndRestoration(t *testing.T) {
	subject, history := readyAgent(t)
	if err := subject.Initialize(); !errors.Is(err, agentloop.ErrState) {
		t.Fatalf("second initialization = %v", err)
	}
	if err := subject.Restore(history.Header(), history.Snapshot()); !errors.Is(err, agentloop.ErrState) {
		t.Fatalf("second restoration = %v", err)
	}
	if err := subject.Release(); !errors.Is(err, agentloop.ErrState) {
		t.Fatalf("release before close = %v", err)
	}
	if _, err := subject.Followup(inputMessage(t, "still available")); err != nil {
		t.Fatal("rejected transitions damaged admission", err)
	}
}

func TestLifecycleRequiresRestoreAndExplicitInitialization(t *testing.T) {
	_, history := readyAgent(t)
	subject := &agentloop.ReactLoopAgent{}
	if err := subject.Initialize(); !errors.Is(err, agentloop.ErrState) {
		t.Fatalf("initialize before restore = %v", err)
	}
	if err := subject.Restore(history.Header(), history.Snapshot()); err != nil {
		t.Fatal(err)
	}

	err := subject.CheckExecution()
	if err == nil {
		t.Fatal("restored Agent became executable before initialization")
	}
	if err := subject.Initialize(); err != nil {
		t.Fatal("initialization", err)
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
	if err := subject.Initialize(); !errors.Is(err, agentloop.ErrState) {
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
	if err := subject.Restore(history.Header(), history.Snapshot()); !errors.Is(err, agentloop.ErrState) {
		t.Fatal("released Agent restored", err)
	}
	if err := subject.Initialize(); !errors.Is(err, agentloop.ErrState) {
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
	write, err := history.Commit(context.Background(), startPlan{
		fact: draft,
	})
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
	if _, err := subject.FinishExecution(); err == nil {
		t.Fatal("committed canceled Turn was not retained for settlement")
	}
}

func TestClosePreservesAcceptedInputBeforeTurnStart(t *testing.T) {
	subject, history := readyAgent(t)
	message := inputMessage(t, "resume after restart")
	proposal, err := subject.Followup(message)
	if err != nil {
		t.Fatal(err)
	}
	write, err := history.Commit(context.Background(), session.Batch(proposal.Edit.Drafts...))
	if err != nil {
		t.Fatal(err)
	}
	_, err = subject.ConfirmAdmission(proposal, write)
	if err != nil {
		t.Fatal(err)
	}
	err = subject.PrepareClose(false)
	if err != nil {
		t.Fatal(err)
	}
	if subject.StatusValue() != agent.StatusIdle {
		t.Fatal("unstarted execution still blocks close")
	}
	if !subject.InboxValue().HasPending() {
		t.Fatal("close discarded input")
	}
	err = subject.Release()
	if err != nil {
		t.Fatal(err)
	}
	restored := &agentloop.ReactLoopAgent{}
	err = restored.Restore(history.Header(), history.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	err = restored.Initialize()
	if err != nil {
		t.Fatal(err)
	}
	if !restored.InboxValue().HasPending() {
		t.Fatal("restoration lost accepted input")
	}
}
