package session

import (
	"context"
	"reflect"
	"testing"
)

// firstWrite uses the first log position as its fixture's stable write identity.
// It has no mutable receipt cache and can resolve a reconstructed log.
type firstWrite struct{ draft EventDraft }

func (plan firstWrite) Build(context.Context, Snapshot) ([]EventDraft, error) {
	return []EventDraft{plan.draft}, nil
}
func (plan firstWrite) FindCommit(snapshot Snapshot) (*WriteResult, error) {
	if len(snapshot.Events) == 0 {
		return nil, nil
	}
	if err := plan.draft.ValidateEvent(snapshot.Events[0]); err != nil {
		return nil, err
	}
	return &WriteResult{
		FirstSeq: 0,
		NextSeq:  1,
		Barrier: WriteBarrier{
			SessionID: snapshot.Barrier.SessionID,
			NextSeq:   1,
		},
		Events: snapshot.Events[:1],
	}, nil
}

type invalidReceipt struct{ result WriteResult }

func (plan invalidReceipt) Build(context.Context, Snapshot) ([]EventDraft, error) {
	panic("replay must not build")
}
func (plan invalidReceipt) FindCommit(Snapshot) (*WriteResult, error) { return &plan.result, nil }

func TestIdempotentCommitReturnsOriginalDetachedResult(t *testing.T) {
	ctx := context.Background()
	history, err := NewLog("idempotent", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plan := firstWrite{
		draft: newFixtureDraft(t, "original"),
	}
	original, err := history.Commit(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(ctx, Batch(newFixtureDraft(t, "later"))); err != nil {
		t.Fatal(err)
	}
	next := history.Seq()
	repeated, err := history.Commit(ctx, plan)
	if err != nil || !reflect.DeepEqual(original, repeated) || history.Seq() != next {
		t.Fatalf("retry changed commitment: %+v, %v", repeated, err)
	}
	repeated.Events[0].Data[0] = '!'
	repeated, err = history.Commit(ctx, plan)
	if err != nil || !reflect.DeepEqual(original, repeated) {
		t.Fatalf("replay aliased log: %v", err)
	}
	if _, err := history.Commit(ctx, firstWrite{
		draft: newFixtureDraft(t, "conflicting content"),
	}); err == nil {
		t.Fatal("conflicting identity accepted")
	}
	if history.Seq() != next {
		t.Fatal("conflict appended an event")
	}
	// Reconstruct from events, without transferring any process-local receipts.
	reconstructed, err := NewLog("idempotent", CreateOptions{
		Seed: history.Snapshot().Events,
	})
	if err != nil {
		t.Fatal(err)
	}
	next = reconstructed.Seq()
	repeated, err = reconstructed.Commit(ctx, plan)
	if err != nil || !reflect.DeepEqual(original, repeated) || reconstructed.Seq() != next {
		t.Fatalf("reconstructed retry: %+v, %v", repeated, err)
	}
}

func TestIdempotentCommitRejectsForgedReceipts(t *testing.T) {
	for _, scenario := range []string{"foreign", "range", "payload", "time"} {
		t.Run(scenario, func(t *testing.T) {
			history, err := NewLog("receipt", CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			original, err := history.Commit(context.Background(), Batch(newFixtureDraft(t, "stored")))
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "foreign":
				original.Barrier.SessionID = "other"
			case "range":
				original.NextSeq++
			case "payload":
				original.Events[0].Data[0] = '!'
			case "time":
				original.Events[0].Time++
			}
			if _, err := history.Commit(context.Background(), invalidReceipt{
				result: original,
			}); err == nil {
				t.Fatal("forged receipt accepted")
			}
			if history.Seq() != 1 {
				t.Fatal("replay changed log")
			}
		})
	}
}
