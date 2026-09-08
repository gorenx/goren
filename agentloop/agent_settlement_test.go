package agentloop_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/session"
)

type endingPlan struct {
	fact agentloop.TurnEnd
}

func (plan endingPlan) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	return plan.fact.Drafts(snapshot)
}

func openTurn(t *testing.T) (*agentloop.ReactLoopAgent, *session.Log) {
	t.Helper()
	subject, history := readyAgent(t)
	proposal, err := subject.Followup(inputMessage(t, "turn input"))
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	draft, err := subject.PrepareTurn()
	if err != nil {
		t.Fatal(err)
	}
	write, err := history.Commit(context.Background(), session.Batch(draft))
	if err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmTurn(write); err != nil {
		t.Fatal(err)
	}
	return subject, history
}

func beginSettlement(t *testing.T, subject *agentloop.ReactLoopAgent, history *session.Log) session.WriteResult {
	t.Helper()
	ending, err := subject.PrepareTurnEnd(errors.New("test: interrupted before Step"))
	if err != nil {
		t.Fatal(err)
	}
	write, err := history.Commit(context.Background(), endingPlan{
		fact: ending,
	})
	if err != nil {
		t.Fatal(err)
	}
	return write
}

func TestSettlementConfirmsCommitAndRetainsBarrierOnFlushFailure(t *testing.T) {
	subject, history := openTurn(t)
	write := beginSettlement(t, subject, history)
	if err := subject.ConfirmTurnEnd(write); err != nil {
		t.Fatal(err)
	}
	boundary, err := subject.TurnBarrier()
	if err != nil || boundary != write.Barrier {
		t.Fatalf("barrier = %+v, %v", boundary, err)
	}
	invalid := boundary
	invalid.NextSeq--
	if err := subject.ConfirmTurnFlush(invalid); err == nil {
		t.Fatal("short flush closed Turn")
	}
	retry, err := subject.TurnBarrier()
	if err != nil || retry != boundary {
		t.Fatalf("retry = %+v, %v", retry, err)
	}
	if _, err := subject.PrepareTurnEnd(errors.New("retry")); err == nil {
		t.Fatal("confirmed end prepared another write")
	}
	if _, err := subject.FinishActivity(); err == nil {
		t.Fatal("unflushed Turn released activity")
	}
	if err := subject.ConfirmTurnFlush(boundary); err != nil {
		t.Fatal(err)
	}
	if _, err := subject.FinishActivity(); err != nil {
		t.Fatal(err)
	}
}

func TestSettlementRecoversLostCommitByDeduplicatingThenReading(t *testing.T) {
	subject, history := openTurn(t)
	write := beginSettlement(t, subject, history)
	ending, err := subject.PrepareTurnEnd(errors.New("transport failed"))
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := history.Commit(context.Background(), endingPlan{
		fact: ending,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repeated.Events) != 0 {
		t.Fatal("retry appended another Turn end")
	}
	if err := subject.ConfirmTurnEnd(repeated); err != nil {
		t.Fatal(err)
	}
	if _, err := subject.TurnBarrier(); err == nil {
		t.Fatal("empty commit invented original event position")
	}
	if err := subject.ReconcileTurnEnd(history.Snapshot()); err != nil {
		t.Fatal(err)
	}
	boundary, err := subject.TurnBarrier()
	if err != nil || boundary != write.Barrier {
		t.Fatalf("recovered barrier = %+v, %v", boundary, err)
	}
}

func TestSettlementRejectsForeignCommitAndPrematureFlush(t *testing.T) {
	subject, history := openTurn(t)
	write := beginSettlement(t, subject, history)
	if err := subject.ConfirmTurnFlush(write.Barrier); err == nil {
		t.Fatal("premature flush succeeded")
	}
	invalid := write
	invalid.Barrier.SessionID = "foreign"
	if err := subject.ConfirmTurnEnd(invalid); err == nil {
		t.Fatal("foreign commit succeeded")
	}
	if _, err := subject.TurnBarrier(); err == nil {
		t.Fatal("invalid confirmation installed barrier")
	}
	if err := subject.ConfirmTurnEnd(write); err != nil {
		t.Fatal(err)
	}
}
