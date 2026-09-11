package agentloop_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

func readyAgent(t *testing.T) (*agentloop.ReactLoopAgent, *session.Log) {
	t.Helper()
	history, err := session.NewLog("admission", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	subject := &agentloop.ReactLoopAgent{}
	if err := subject.Restore(history.Header(), history.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := subject.Initialize(); err != nil {
		t.Fatal(err)
	}
	return subject, history
}

func inputMessage(t *testing.T, text string) agentmessage.UserMessage {
	t.Helper()
	input, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock(text)},
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func confirmInput(t *testing.T, subject *agentloop.ReactLoopAgent, history *session.Log, proposal agent.Admission) {
	t.Helper()
	write, err := history.Commit(context.Background(), session.Batch(proposal.Edit.Drafts...))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := subject.ConfirmAdmission(proposal, write); err != nil {
		t.Fatal(err)
	}
}

func TestBusinessInputRequiresExactCommitBeforeWake(t *testing.T) {
	for _, kind := range []string{"identity", "range", "payload"} {
		t.Run(kind, func(t *testing.T) {
			subject, history := readyAgent(t)
			var api agent.Agent = subject
			proposal, err := api.Followup(inputMessage(t, "accepted"))
			if err != nil {
				t.Fatal(err)
			}
			if api.StatusValue() != agent.StatusIdle || api.InboxValue().HasPending() {
				t.Fatal("proposal changed business state")
			}
			write, err := history.Commit(context.Background(), session.Batch(proposal.Edit.Drafts...))
			if err != nil {
				t.Fatal(err)
			}
			malformed := write
			malformed.Events = append([]session.Event(nil), write.Events...)
			switch kind {
			case "identity":
				malformed.Barrier.SessionID = "another"
			case "range":
				malformed.NextSeq++
			case "payload":
				malformed.Events[0].Data = []byte(`{}`)
			}
			if _, err := subject.ConfirmAdmission(proposal, malformed); err == nil {
				t.Fatal("invalid commit accepted")
			}
			if api.StatusValue() != agent.StatusIdle || api.InboxValue().HasPending() {
				t.Fatal("failed confirmation changed state")
			}
			if _, err := subject.ConfirmAdmission(proposal, write); err != nil {
				t.Fatal(err)
			}
			if api.ID() != "admission" || api.StatusValue() != agent.StatusRunning || len(api.InboxValue().NextTurn()) != 1 {
				t.Fatal("confirmed input did not reserve execution")
			}
		})
	}
}

func TestInjectDoesNotWakeAndCanceledSteerTargetsNextTurn(t *testing.T) {
	subject, history := readyAgent(t)
	proposal, err := subject.Inject(inputMessage(t, "context"))
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	if subject.StatusValue() != agent.StatusIdle || len(subject.InboxValue().NextStep()) != 1 {
		t.Fatal("Inject woke execution or changed target")
	}
	proposal, err = subject.Steer(inputMessage(t, "steer"))
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	if len(subject.InboxValue().NextStep()) != 2 {
		t.Fatal("ordinary Steer target")
	}
	if _, err := subject.Cancel(agent.ParentCancel{}, agent.CancelOptions{
		KeepInbox: true,
	}); err != nil {
		t.Fatal(err)
	}
	proposal, err = subject.Steer(inputMessage(t, "after cancellation"))
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	if len(subject.InboxValue().NextTurn()) != 1 {
		t.Fatal("canceled Steer did not target next Turn")
	}
	var stopped agent.Cancellation
	if !errors.As(subject.Cancellation(), &stopped) {
		t.Fatal("cancellation lost error type")
	}
	if _, ok := stopped.Cause.(agent.ParentCancel); !ok {
		t.Fatal("cancellation lost cause")
	}
	replay, err := subject.FinishExecution()
	if err != nil || !replay || subject.Cancellation() != nil || subject.StatusValue() != agent.StatusRunning {
		t.Fatalf("replay = %v, %v", replay, err)
	}
}

func TestRemovingInputDoesNotEraseExecutionReservation(t *testing.T) {
	subject, history := readyAgent(t)
	input := inputMessage(t, "removed after wake")
	proposal, err := subject.Followup(input)
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	edit, err := subject.Remove(input.StableID())
	if err != nil {
		t.Fatal(err)
	}
	write, err := history.Commit(context.Background(), session.Batch(edit.Drafts...))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := subject.ConfirmEdit(edit, write); err != nil {
		t.Fatal(err)
	}
	if _, err := subject.PrepareTurn(); err != nil {
		t.Fatal("reserved execution lost initial Turn", err)
	}
	if err := subject.PrepareClose(false); !errors.Is(err, agentloop.ErrPending) {
		t.Fatal("empty Inbox allowed premature close", err)
	}
}

func TestCancelClearsInboxOnlyAfterCommitAndSuppressesReplay(t *testing.T) {
	subject, history := readyAgent(t)
	proposal, err := subject.Followup(inputMessage(t, "pending"))
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	edit, err := subject.Cancel(agent.UserCancel{}, agent.CancelOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !subject.InboxValue().HasPending() {
		t.Fatal("cancel removed uncommitted input")
	}
	write, err := history.Commit(context.Background(), session.Batch(edit.Drafts...))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := subject.ConfirmEdit(edit, write); err != nil {
		t.Fatal(err)
	}
	if _, err := subject.PrepareTurn(); err == nil {
		t.Fatal("canceled execution opened Turn")
	}
	replay, err := subject.FinishExecution()
	if err != nil || replay || subject.StatusValue() != agent.StatusIdle {
		t.Fatalf("finish = %v, %v", replay, err)
	}
}

func TestPromoteCommitsOneCompleteMove(t *testing.T) {
	subject, history := readyAgent(t)
	input := inputMessage(t, "promote")
	admission, err := subject.Followup(input)
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, admission)
	proposal, err := subject.Promote(input.StableID())
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Edit.Drafts) != 2 {
		t.Fatalf("move drafts: %d", len(proposal.Edit.Drafts))
	}
	pending, _ := subject.Inbox()
	if len(pending.NextTurn) != 1 || len(pending.NextStep) != 0 {
		t.Fatal("proposal mutated live state")
	}
	before := history.Seq()
	confirmInput(t, subject, history, proposal)
	pending, _ = subject.Inbox()
	if len(pending.NextTurn) != 0 || len(pending.NextStep) != 1 || pending.NextStep[0].StableID() != input.StableID() {
		t.Fatalf("move: %+v", pending)
	}
	if history.Seq() != before+2 {
		t.Fatal("move was not committed as a complete pair")
	}
	if _, err := subject.Promote(input.StableID()); !errors.Is(err, agent.ErrSteerUnavailable) {
		t.Fatalf("next-step promotion: %v", err)
	}
	if _, err := subject.Promote("missing"); !errors.Is(err, agent.ErrInputMissing) {
		t.Fatalf("missing promotion: %v", err)
	}
}

func TestPromoteAfterCancellationKeepsInputForNextTurn(t *testing.T) {
	subject, history := readyAgent(t)
	first := inputMessage(t, "first")
	second := inputMessage(t, "second")
	for _, input := range []agentmessage.UserMessage{first, second} {
		proposal, err := subject.Followup(input)
		if err != nil {
			t.Fatal(err)
		}
		confirmInput(t, subject, history, proposal)
	}
	if _, err := subject.Cancel(agent.UserCancel{}, agent.CancelOptions{
		KeepInbox: true,
	}); err != nil {
		t.Fatal(err)
	}
	proposal, err := subject.Promote(first.StableID())
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	pending, err := subject.Inbox()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.NextStep) != 0 || len(pending.NextTurn) != 2 || pending.NextTurn[0].StableID() != second.StableID() || pending.NextTurn[1].StableID() != first.StableID() {
		t.Fatalf("canceled steering changed placement: %+v", pending)
	}
}
