package basic

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func TestTransactionCancellationBeforeGenerationOnlyCommitsFailedEnd(t *testing.T) {
	for _, beforeStart := range []bool{true, false} {
		history, execution := summaryTransaction(t)
		cause := errors.New("cancelled before generation")
		if beforeStart {
			proposal, err := execution.Write()
			if err != nil {
				t.Fatal(err)
			}
			if err := execution.Cancel(cause); err != nil {
				t.Fatal(err)
			}
			retained, err := execution.Write()
			if err != nil || retained != proposal {
				t.Fatal("cancel replaced unconfirmed start")
			}
		}
		commitTransaction(t, history, execution)
		if !beforeStart {
			if err := execution.Cancel(cause); err != nil {
				t.Fatal(err)
			}
		}
		if execution.Stage() != CommitPending {
			t.Fatal("cancelled transaction scheduled generation")
		}
		if _, err := execution.Generation(); err == nil {
			t.Fatal("generation accepted after cancel")
		}
		commitTransaction(t, history, execution)
		if _, err := execution.Result(); err == nil {
			t.Fatal("cancel bypassed manual flush")
		}
		if err := execution.ConfirmFlush(history.Snapshot().Barrier, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := execution.Result(); !errors.Is(err, cause) {
			t.Fatalf("result: %v", err)
		}
		assertCancelledHistory(t, history)
	}
}

func TestTransactionCancellationDiscardsLateStreamOutput(t *testing.T) {
	history, execution := summaryTransaction(t)
	commitTransaction(t, history, execution)
	if _, err := execution.Generation(); err != nil {
		t.Fatal(err)
	}
	if err := execution.Opened(llm.CallConfig{
		Provider: "summary",
		Model:    "checkpoint",
	}, nil); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("user stopped summary")
	if err := execution.Cancel(cause); err != nil {
		t.Fatal(err)
	}
	if err := execution.Cancel(errors.New("later cancellation")); err != nil {
		t.Fatal(err)
	}
	if err := execution.Push(llm.TextDeltaChunk{
		Index: 0,
		Text:  "late output",
	}); !errors.Is(err, cause) {
		t.Fatalf("late output accepted: %v", err)
	}
	if execution.Stage() != SummaryClosing {
		t.Fatal("cancel continued stream")
	}
	if _, err := execution.Write(); err == nil {
		t.Fatal("cancel bypassed resource close")
	}
	if err := execution.ConfirmClose(nil); err != nil {
		t.Fatal(err)
	}
	commitTransaction(t, history, execution)
	if err := execution.ConfirmFlush(session.WriteBarrier{}, errors.New("disk full")); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.Result(); !errors.Is(err, cause) {
		t.Fatalf("flush overrode cancel: %v", err)
	}
	assertCancelledHistory(t, history)
}

func assertCancelledHistory(t *testing.T, history *session.Log) {
	t.Helper()
	snapshot := history.Snapshot()
	if snapshot.Surface.ReplaceGeneration != 0 {
		t.Fatal("cancelled summary replaced Surface")
	}
	for _, entry := range snapshot.Events {
		if entry.Type == compaction.SummaryEventName {
			t.Fatal("cancelled summary was committed")
		}
	}
	state, err := compaction.InspectLog(snapshot.Events)
	if err != nil || state.Attempt != nil {
		t.Fatalf("unfinished cancellation: %#v, %v", state, err)
	}
	last := snapshot.Events[len(snapshot.Events)-1]
	ending, err := compaction.DecodeEnd(last.Data)
	if last.Type != compaction.EndEventName || err != nil || ending.Error == nil {
		t.Fatal("cancelled transaction has no failed end")
	}
}

func TestCancellationDuringFlushPreservesAlreadyCommittedSummary(t *testing.T) {
	history, execution := summaryTransaction(t)
	commitTransaction(t, history, execution)
	if _, err := execution.Generation(); err != nil {
		t.Fatal(err)
	}
	if err := execution.Opened(llm.CallConfig{
		Provider: "summary",
		Model:    "checkpoint",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := execution.Push(llm.TextDeltaChunk{
		Index: 0,
		Text:  "checkpoint",
	}); err != nil {
		t.Fatal(err)
	}
	if err := execution.EndGeneration(nil); err != nil {
		t.Fatal(err)
	}
	if err := execution.ConfirmClose(nil); err != nil {
		t.Fatal(err)
	}
	commitTransaction(t, history, execution)
	before := history.Snapshot()
	cause := errors.New("cancelled during flush")
	if err := execution.Cancel(cause); err != nil {
		t.Fatal(err)
	}
	if execution.Stage() != FlushPending {
		t.Fatal("cancel bypassed pending flush")
	}
	if err := execution.ConfirmFlush(before.Barrier, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.Result(); !errors.Is(err, cause) {
		t.Fatalf("result: %v", err)
	}
	if !reflect.DeepEqual(before, history.Snapshot()) {
		t.Fatal("cancel rewrote committed summary")
	}
}
