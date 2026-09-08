package basic

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

func flowPrices(snapshot session.Snapshot, total int64) tokenmeter.Measurement {
	prices := tokenmeter.Measurement{
		LogRevision: snapshot.Barrier.NextSeq,
		TotalTokens: total,
	}
	for _, seq := range snapshot.Surface.Nodes {
		prices.Nodes = append(prices.Nodes, tokenmeter.SurfaceNode{
			Seq:    seq,
			Tokens: 10,
		})
	}
	return prices
}

func flowOwner(t *testing.T) *Compaction {
	t.Helper()
	settings, err := ResolveConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewCompaction(settings, &checkpointEstimate{
		tokens: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func flowRoute(t *testing.T, history *session.Log) {
	t.Helper()
	turn, err := session.NewEventDraft(session.TurnStarted, session.TurnStart{
		Turn: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	header, err := session.NewEventDraft(session.RequestHeaderSet, session.RequestHeaderSnapshot{
		Header: session.EpochHeader{
			Config: llm.CallConfig{
				Provider: "summary",
				Model:    "checkpoint",
			},
		},
		Reason: session.RequestHeaderInitial,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(context.Background(), session.Batch(turn, header)); err != nil {
		t.Fatal(err)
	}
}

func TestManualFlowSkipsPruningAndWaitsForDurableTransaction(t *testing.T) {
	for _, flushFailure := range []error{nil, errors.New("disk full")} {
		history, _ := transactionSession(t)
		owner := flowOwner(t)
		snapshot := history.Snapshot()
		input := Invocation{
			Mode:        Manual,
			Snapshot:    snapshot,
			Measurement: flowPrices(snapshot, 30),
			Prune:       true,
			Fallback: RouteTarget{
				Provider: "summary",
				Model:    "checkpoint",
			},
		}
		if err := owner.Begin(input); err != nil {
			t.Fatal(err)
		}
		if owner.Stage() != TransactionPending {
			t.Fatalf("manual stage = %v", owner.Stage())
		}
		if err := owner.Begin(input); err == nil {
			t.Fatal("concurrent invocation accepted")
		}
		if err := owner.CompleteTransaction(snapshot); err == nil {
			t.Fatal("unfinished transaction accepted")
		}
		execution, err := owner.CurrentTransaction()
		if err != nil {
			t.Fatal(err)
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
		if err := owner.CompleteTransaction(history.Snapshot()); err == nil {
			t.Fatal("manual completion before flush")
		}
		if err := execution.ConfirmFlush(history.Snapshot().Barrier, flushFailure); err != nil {
			t.Fatal(err)
		}
		if err := owner.CompleteTransaction(snapshot); err == nil {
			t.Fatal("stale snapshot accepted")
		}
		if err := owner.CompleteTransaction(history.Snapshot()); err != nil {
			t.Fatal(err)
		}
		outcome, err := owner.Result()
		if !errors.Is(err, flushFailure) || !outcome.Changed || (outcome.Latest != nil) != (flushFailure == nil) {
			t.Fatalf("outcome = %#v, %v", outcome, err)
		}

		input.Snapshot = history.Snapshot()
		input.Measurement = flowPrices(input.Snapshot, 20)
		if err := owner.Begin(input); err != nil {
			t.Fatalf("settled maintenance blocked next invocation: %v", err)
		}
	}
}

func TestPressureFlowPrunesOnlyAboveThresholdAndRemeasures(t *testing.T) {
	for _, total := range []int64{79, 80} {
		history, _ := transactionSession(t)
		flowRoute(t, history)
		owner := flowOwner(t)
		snapshot := history.Snapshot()
		capacity := 100
		if err := owner.Begin(Invocation{
			Mode:          Pressure,
			Snapshot:      snapshot,
			Measurement:   flowPrices(snapshot, total),
			ContextWindow: &capacity,
			Prune:         true,
		}); err != nil {
			t.Fatal(err)
		}
		if total == 79 {
			if owner.Stage() != CompactionFinished {
				t.Fatal("low pressure invoked pruning")
			}
			continue
		}
		if owner.Stage() != PrunePending {
			t.Fatal("high pressure skipped pruning")
		}
		if err := owner.Pruned(snapshot, nil); err != nil {
			t.Fatal(err)
		}
		mismatched := flowPrices(snapshot, 79)
		mismatched.LogRevision--
		if err := owner.Measured(snapshot, mismatched); err == nil {
			t.Fatal("mixed revisions accepted")
		}
		if err := owner.Measured(snapshot, flowPrices(snapshot, 79)); err != nil {
			t.Fatal(err)
		}
		if owner.Stage() != CompactionFinished {
			t.Fatal("remeasured low pressure started summary")
		}
	}
}

func TestOverflowFlowDoesNotRequireCapacityOrUseFallbackAsRoute(t *testing.T) {
	history, _ := transactionSession(t)
	owner := flowOwner(t)
	snapshot := history.Snapshot()
	input := Invocation{
		Mode:        Overflow,
		Snapshot:    snapshot,
		Measurement: flowPrices(snapshot, 1000),
		Prune:       true,
		Fallback: RouteTarget{
			Provider: "summary",
			Model:    "checkpoint",
		},
	}
	if err := owner.Begin(input); err != nil {
		t.Fatal(err)
	}
	if owner.Stage() != CompactionFinished {
		t.Fatal("fallback started automatic compaction without durable route")
	}
	flowRoute(t, history)
	input.Snapshot = history.Snapshot()
	input.Measurement = flowPrices(input.Snapshot, 1000)
	if err := owner.Begin(input); err != nil {
		t.Fatal(err)
	}
	if owner.Stage() != PrunePending {
		t.Fatal("overflow required model capacity")
	}
	if err := owner.Pruned(input.Snapshot, nil); err != nil {
		t.Fatal(err)
	}
	if err := owner.Measured(input.Snapshot, input.Measurement); err != nil {
		t.Fatal(err)
	}
	if owner.Stage() != TransactionPending {
		t.Fatalf("overflow stage = %v", owner.Stage())
	}
}

func TestPressureFlowStopsAtConfiguredSummaryLimit(t *testing.T) {
	history, _ := transactionSession(t)
	flowRoute(t, history)
	settings, err := ResolveConfig(Config{
		PolicyConfig: PolicyConfig{
			RetainTokens: int64Pointer(0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewCompaction(settings, &checkpointEstimate{
		tokens: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	capacity := 100
	snapshot := history.Snapshot()
	if err := owner.Begin(Invocation{
		Mode:          Pressure,
		Snapshot:      snapshot,
		Measurement:   flowPrices(snapshot, 100),
		ContextWindow: &capacity,
	}); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt <= settings.CompactionRetries; attempt++ {
		execution, err := owner.CurrentTransaction()
		if err != nil {
			t.Fatal(err)
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
		if execution.Stage() != TransactionFinished {
			t.Fatal("automatic transaction required manual flush")
		}
		snapshot = history.Snapshot()
		if err := owner.CompleteTransaction(snapshot); err != nil {
			t.Fatal(err)
		}
		if err := owner.Measured(snapshot, flowPrices(snapshot, 100)); err != nil {
			t.Fatal(err)
		}
	}
	if owner.Stage() != CompactionFinished {
		t.Fatal("summary budget did not terminate flow")
	}
	outcome, err := owner.Result()
	if err == nil || !outcome.Changed || outcome.Latest == nil {
		t.Fatalf("budget outcome = %#v, %v", outcome, err)
	}
}

func TestMeasurementFailurePreservesCommittedSummary(t *testing.T) {
	history, _ := transactionSession(t)
	flowRoute(t, history)
	settings, err := ResolveConfig(Config{
		PolicyConfig: PolicyConfig{
			RetainTokens: int64Pointer(0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewCompaction(settings, &checkpointEstimate{
		tokens: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("measurement rejected")
	if err := owner.RejectMeasurement(failure); err == nil {
		t.Fatal("idle invocation accepted measurement failure")
	}
	snapshot := history.Snapshot()
	capacity := 100
	if err := owner.Begin(Invocation{
		Mode:          Pressure,
		Snapshot:      snapshot,
		Measurement:   flowPrices(snapshot, 100),
		ContextWindow: &capacity,
	}); err != nil {
		t.Fatal(err)
	}
	execution, err := owner.CurrentTransaction()
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.RejectMeasurement(failure); err == nil {
		t.Fatal("measurement failure bypassed active transaction")
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
	snapshot = history.Snapshot()
	if err := owner.CompleteTransaction(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := owner.RejectMeasurement(nil); err == nil {
		t.Fatal("nil rejection accepted")
	}
	if err := owner.RejectMeasurement(failure); err != nil {
		t.Fatal(err)
	}
	outcome, err := owner.Result()
	if !errors.Is(err, failure) || !outcome.Changed || outcome.Latest == nil {
		t.Fatalf("settled outcome = %#v, %v", outcome, err)
	}
	if err := owner.RejectMeasurement(failure); err == nil {
		t.Fatal("late rejection accepted after settlement")
	}
	if err := owner.Begin(Invocation{
		Mode:          Pressure,
		Snapshot:      snapshot,
		Measurement:   flowPrices(snapshot, 1),
		ContextWindow: &capacity,
	}); err != nil {
		t.Fatal(err)
	}
	outcome, err = owner.Result()
	if err != nil || outcome.Changed || outcome.Latest != nil {
		t.Fatalf("new invocation inherited old outcome = %#v, %v", outcome, err)
	}
}
