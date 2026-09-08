package basic

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/compaction"
)

func TestTransactionEndReplaysFailureAndDoesNotReopenStart(t *testing.T) {
	history, selected := transactionSession(t)
	lifecycle := compaction.Start{
		CompactionID: "transaction",
	}
	start, err := newTransactionStart(lifecycle, selected)
	if err != nil {
		t.Fatal(err)
	}
	began, err := history.Commit(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	end, err := newTransactionEnd(lifecycle, errors.New("summary failed"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := history.Commit(context.Background(), end)
	if err != nil {
		t.Fatal(err)
	}
	before := history.Snapshot().Barrier.NextSeq
	for range 2 {
		repeated, err := history.Commit(context.Background(), end)
		if err != nil || !reflect.DeepEqual(first, repeated) {
			t.Fatalf("repeated end = %#v, %v", repeated, err)
		}
	}
	replayed, err := history.Commit(context.Background(), start)
	if err != nil || !reflect.DeepEqual(began, replayed) {
		t.Fatalf("repeated start = %#v, %v", replayed, err)
	}
	if history.Snapshot().Barrier.NextSeq != before {
		t.Fatal("retries changed log")
	}
	state, err := compaction.InspectLog(history.Snapshot().Events)
	if err != nil || state.Attempt != nil {
		t.Fatalf("transaction reopened: %#v, %v", state, err)
	}
	conflicting, err := newTransactionEnd(lifecycle, errors.New("different failure"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(context.Background(), conflicting); err == nil {
		t.Fatal("existing end overwritten")
	}
	if history.Snapshot().Barrier.NextSeq != before {
		t.Fatal("conflict changed log")
	}
}

func TestTransactionEndRequiresMatchingStartedTransaction(t *testing.T) {
	history, selected := transactionSession(t)
	lifecycle := compaction.Start{
		CompactionID: "transaction",
	}
	if _, err := newTransactionEnd(lifecycle, nil); err == nil {
		t.Fatal("standalone success end accepted")
	}
	end, err := newTransactionEnd(lifecycle, errors.New("failed"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(context.Background(), end); err == nil {
		t.Fatal("end without start accepted")
	}
	origin := "original"
	lifecycle.SourceCommandID = &origin
	start, err := newTransactionStart(lifecycle, selected)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	before := history.Snapshot().Barrier.NextSeq
	if _, err := history.Commit(context.Background(), end); err == nil {
		t.Fatal("mismatched source accepted")
	}
	if history.Snapshot().Barrier.NextSeq != before {
		t.Fatal("invalid end changed log")
	}
}
