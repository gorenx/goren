package agentloop_test

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agentloop"
	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	"github.com/gorenx/goren/session"
)

type stepEndPlan struct{ fact agentloop.EndStep }

func (plan stepEndPlan) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	return plan.fact.Drafts(snapshot)
}
func (plan stepEndPlan) FindCommit(snapshot session.Snapshot) (*session.WriteResult, error) {
	return plan.fact.Find(snapshot)
}

func TestStepEndRejectsForeignAndInvalidLogBoundaries(t *testing.T) {
	subject, history := requestingStep(t)
	ending, err := subject.PrepareStepEnd(stepstate.StepResultCompleted)
	if err != nil {
		t.Fatal(err)
	}
	initial := history.Snapshot()
	foreign := initial
	foreign.Barrier.SessionID = "other"
	_, err = ending.Find(foreign)
	if err == nil {
		t.Fatal("foreign Session accepted")
	}
	incomplete := initial
	incomplete.Events = incomplete.Events[1:]
	_, err = ending.Find(incomplete)
	if err == nil {
		t.Fatal("incomplete log accepted")
	}
	write, err := history.Commit(context.Background(), stepEndPlan{fact: ending})
	if err != nil {
		t.Fatal(err)
	}
	retried, err := history.Commit(context.Background(), stepEndPlan{fact: ending})
	if err != nil {
		t.Fatal(err)
	}
	if write.FirstSeq != retried.FirstSeq || history.Snapshot().Barrier.NextSeq != write.NextSeq {
		t.Fatal("retry appended a duplicate Step end")
	}
	duplicate := history.Snapshot()
	entry := write.Events[0]
	entry.Seq = duplicate.Barrier.NextSeq
	duplicate.Events = append(duplicate.Events, entry)
	duplicate.Barrier.NextSeq++
	_, err = ending.Find(duplicate)
	if err == nil {
		t.Fatal("duplicate end accepted")
	}
	_, err = ending.Drafts(history.Snapshot())
	if err == nil {
		t.Fatal("closed Step accepted a new end draft")
	}
}

func TestStepEndRejectsLogThatMovedPastItsOpenStep(t *testing.T) {
	subject, history := requestingStep(t)
	ending, err := subject.PrepareStepEnd(stepstate.StepResultCompleted)
	if err != nil {
		t.Fatal(err)
	}
	next, err := session.NewEventDraft(session.StepStarted, session.StepPosition{
		Turn: 1,
		Step: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = history.Commit(context.Background(), session.Batch(next))
	if err != nil {
		t.Fatal(err)
	}
	_, err = history.Commit(context.Background(), stepEndPlan{fact: ending})
	if err == nil {
		t.Fatal("stale Step end appended after another Step start")
	}
}
