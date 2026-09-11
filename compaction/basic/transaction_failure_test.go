package basic

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
)

func TestTransactionInvalidSummaryEndsWithoutReplacingHistory(t *testing.T) {
	for _, scenario := range []string{"truncated", "not smaller"} {
		t.Run(scenario, func(t *testing.T) {
			history, execution := summaryTransaction(t)
			before := history.Snapshot().Surface
			if scenario == "not smaller" {
				execution.pricing = &checkpointEstimate{
					tokens: 20,
				}
			}
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
				Text:  "summary",
			}); err != nil {
				t.Fatal(err)
			}
			if scenario == "truncated" {
				if err := execution.Push(llm.FinishChunk{
					Reason: llm.MaxTokensFinish{},
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := execution.EndGeneration(nil); err != nil {
				t.Fatal(err)
			}
			if err := execution.ConfirmClose(nil); err != nil {
				t.Fatal(err)
			}
			commitTransaction(t, history, execution)
			if err := execution.ConfirmFlush(history.Snapshot().Barrier, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := execution.Result(); err == nil {
				t.Fatal("invalid summary succeeded")
			}
			snapshot := history.Snapshot()
			if !reflect.DeepEqual(before, snapshot.Surface) {
				t.Fatal("invalid summary replaced history")
			}
			ending, err := compaction.DecodeEnd(snapshot.Events[len(snapshot.Events)-1].Data)
			if err != nil || ending.Error == nil {
				t.Fatalf("failure end = %#v %v", ending, err)
			}
			state, err := compaction.InspectLog(snapshot.Events)
			if err != nil || state.Attempt != nil {
				t.Fatalf("transaction left open: %#v %v", state, err)
			}
		})
	}
}

func TestCompactionRejectsExistingLockBeforePruning(t *testing.T) {
	history, selected := transactionSession(t)
	flowRoute(t, history)
	proposal, err := newTransactionStart(compaction.Start{
		CompactionID: "other",
		Turn:         int64Pointer(1),
	}, selected)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(context.Background(), proposal); err != nil {
		t.Fatal(err)
	}
	snapshot := history.Snapshot()
	capacity := 100
	owner := flowOwner(t)
	err = owner.Begin(Invocation{
		Mode:          Pressure,
		Snapshot:      snapshot,
		Measurement:   flowPrices(snapshot, 100),
		ContextWindow: &capacity,
		Prune:         true,
	})
	var busy *compaction.ManualError
	if !errors.As(err, &busy) || busy.Code != compaction.ManualErrorBusy {
		t.Fatal(err)
	}
	if owner.Stage() != CompactionIdle {
		t.Fatal("conflicting lock started work")
	}
}

func TestPrunerFailureStopsBeforeAnySummaryTransaction(t *testing.T) {
	history, _ := transactionSession(t)
	flowRoute(t, history)
	snapshot := history.Snapshot()
	owner := flowOwner(t)
	if err := owner.Begin(Invocation{
		Mode:        Overflow,
		Snapshot:    snapshot,
		Measurement: flowPrices(snapshot, 100),
		Prune:       true,
	}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("pruning failed")
	if err := owner.Pruned(snapshot, failure); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.CurrentTransaction(); err == nil {
		t.Fatal("summary follows failed pruning")
	}
	outcome, err := owner.Result()
	if !errors.Is(err, failure) || outcome.Changed || outcome.Latest != nil {
		t.Fatalf("outcome = %#v %v", outcome, err)
	}
}
