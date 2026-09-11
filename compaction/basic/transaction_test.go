package basic

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

func summaryTransaction(t *testing.T) (*session.Log, *Transaction) {
	t.Helper()
	history, selected := transactionSession(t)
	snapshot := history.Snapshot()
	measurement := tokenmeter.Measurement{
		LogRevision: snapshot.Barrier.NextSeq,
	}
	for _, seq := range snapshot.Surface.Nodes {
		measurement.Nodes = append(measurement.Nodes, tokenmeter.SurfaceNode{
			Seq:    seq,
			Tokens: 10,
		})
	}
	settings, err := ResolveConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := NewTransaction(settings, TransactionInput{
		Lifecycle: compaction.Start{
			CompactionID: "transaction",
		},
		Snapshot:    snapshot,
		Measurement: measurement,
		Range:       selected,
		Fallback: RouteTarget{
			Provider: "summary",
			Model:    "checkpoint",
		},
	}, &checkpointEstimate{
		tokens: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return history, execution
}

func commitTransaction(t *testing.T, history *session.Log, execution *Transaction) {
	t.Helper()
	proposal, err := execution.Write()
	if err != nil {
		t.Fatal(err)
	}
	written, err := history.Commit(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	// An invalid receipt must leave the exact proposal pending for retry.
	invalid := written
	invalid.Barrier.NextSeq++
	if err := execution.AcceptCommit(invalid, nil); err == nil {
		t.Fatal("wrong barrier accepted")
	}
	if _, err := execution.Write(); err != nil {
		t.Fatal(err)
	}
	if err := execution.AcceptCommit(written, nil); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionWaitsForCloseAndFlush(t *testing.T) {
	history, execution := summaryTransaction(t)
	if _, err := execution.Generation(); err == nil {
		t.Fatal("generation before start")
	}
	commitTransaction(t, history, execution)
	if _, err := execution.Generation(); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.Generation(); err == nil {
		t.Fatal("duplicate generation")
	}
	if err := execution.Opened(llm.CallConfig{
		Provider: "summary",
		Model:    "checkpoint",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := execution.Push(llm.TextDeltaChunk{
		Index: 0,
		Text:  "short summary",
	}); err != nil {
		t.Fatal(err)
	}
	if err := execution.EndGeneration(nil); err != nil {
		t.Fatal(err)
	}
	closing := errors.New("close failed")
	if err := execution.ConfirmClose(closing); !errors.Is(err, closing) {
		t.Fatal(err)
	}
	if execution.Stage() != SummaryClosing {
		t.Fatal("failed close advanced transaction")
	}
	if _, err := execution.Write(); err == nil {
		t.Fatal("write before close")
	}
	if err := execution.ConfirmClose(nil); err != nil {
		t.Fatal(err)
	}
	commitTransaction(t, history, execution)
	if _, err := execution.Result(); err == nil {
		t.Fatal("manual success before flush")
	}
	barrier, err := execution.FlushBarrier()
	if err != nil || barrier != history.Snapshot().Barrier {
		t.Fatalf("flush = %#v, %v", barrier, err)
	}
	if err := execution.ConfirmFlush(history.Snapshot().Barrier, nil); err != nil {
		t.Fatal(err)
	}
	result, err := execution.Result()
	if err != nil || result == nil {
		t.Fatalf("result = %#v, %v", result, err)
	}
}

func TestTransactionOpeningFailureClosesAndPersistsFailedEnd(t *testing.T) {
	history, execution := summaryTransaction(t)
	commitTransaction(t, history, execution)
	if _, err := execution.Generation(); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("provider opening failed")
	if err := execution.Opened(llm.CallConfig{}, failure); err != nil {
		t.Fatal(err)
	}
	if err := execution.ConfirmClose(nil); err != nil {
		t.Fatal(err)
	}
	rejected := errors.New("end rejected")
	if err := execution.AcceptCommit(session.WriteResult{}, rejected); !errors.Is(err, rejected) || !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if execution.Stage() != CommitPending {
		t.Fatal("rejected end finished transaction")
	}
	commitTransaction(t, history, execution)
	if err := execution.ConfirmFlush(history.Snapshot().Barrier, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.Result(); !errors.Is(err, failure) {
		t.Fatalf("lost original cause: %v", err)
	}
	state, err := compaction.InspectLog(history.Snapshot().Events)
	if err != nil || state.Attempt != nil {
		t.Fatalf("unfinished transaction: %#v %v", state, err)
	}
}

func TestTransactionRequiresActualDurablePrefix(t *testing.T) {
	history, execution := summaryTransaction(t)
	commitTransaction(t, history, execution)
	if _, err := execution.Generation(); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("opening failed")
	if err := execution.Opened(llm.CallConfig{}, failure); err != nil {
		t.Fatal(err)
	}
	if err := execution.ConfirmClose(nil); err != nil {
		t.Fatal(err)
	}
	commitTransaction(t, history, execution)
	needed, err := execution.FlushBarrier()
	if err != nil {
		t.Fatal(err)
	}
	for _, durable := range []session.WriteBarrier{
		{
			SessionID: "other",
			NextSeq:   needed.NextSeq,
		},
		{
			SessionID: needed.SessionID,
			NextSeq:   needed.NextSeq - 1,
		},
	} {
		if err := execution.ConfirmFlush(durable, nil); err == nil {
			t.Fatal("invalid durable prefix accepted")
		}
		if execution.Stage() != FlushPending {
			t.Fatal("invalid flush finished transaction")
		}
	}
	if err := execution.ConfirmFlush(needed, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.Result(); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}

func TestTransactionPreservesProposalWhenCommitReturnsEventsAndError(t *testing.T) {
	history, execution := summaryTransaction(t)
	proposal, err := execution.Write()
	if err != nil {
		t.Fatal(err)
	}
	written, err := history.Commit(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("uncertain acknowledgement")
	if err := execution.AcceptCommit(written, failure); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	retained, err := execution.Write()
	if err != nil || retained != proposal || execution.Stage() != StartPending {
		t.Fatal("uncertain outcome replaced proposal")
	}
	if err := execution.AcceptCommit(written, nil); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionFlushFailureSettlesWithoutChangingCommittedEvents(t *testing.T) {
	for _, priorFailure := range []bool{false, true} {
		name := "summary committed"
		if priorFailure {
			name = "summary failed"
		}
		t.Run(name, func(t *testing.T) {
			history, execution := summaryTransaction(t)
			commitTransaction(t, history, execution)
			if _, err := execution.Generation(); err != nil {
				t.Fatal(err)
			}
			var original error
			if priorFailure {
				original = errors.New("summary provider failed")
			}
			if err := execution.Opened(llm.CallConfig{
				Provider: "summary",
				Model:    "checkpoint",
			}, original); err != nil {
				t.Fatal(err)
			}
			if !priorFailure {
				if err := execution.Push(llm.TextDeltaChunk{
					Index: 0,
					Text:  "short summary",
				}); err != nil {
					t.Fatal(err)
				}
				if err := execution.EndGeneration(nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := execution.ConfirmClose(nil); err != nil {
				t.Fatal(err)
			}
			commitTransaction(t, history, execution)
			before := history.Snapshot()
			diskFailure := errors.New("disk full")
			if err := execution.ConfirmFlush(session.WriteBarrier{}, diskFailure); err != nil {
				t.Fatal(err)
			}
			if execution.Stage() != TransactionFinished {
				t.Fatal("definite flush failure retained maintenance")
			}
			result, err := execution.Result()
			if result != nil {
				t.Fatal("flush failure reported success")
			}
			if priorFailure {
				if !errors.Is(err, original) || errors.Is(err, diskFailure) {
					t.Fatalf("failure precedence = %v", err)
				}
			} else {
				var manual *compaction.ManualError
				if !errors.As(err, &manual) || manual.Code != compaction.ManualErrorPersistence || !errors.Is(err, diskFailure) {
					t.Fatalf("persistence failure = %v", err)
				}
			}
			if !reflect.DeepEqual(before, history.Snapshot()) {
				t.Fatal("flush failure rewrote committed events")
			}
			if _, err := execution.FlushBarrier(); err == nil {
				t.Fatal("settled failure requested another flush")
			}
			if err := execution.ConfirmFlush(before.Barrier, nil); err == nil {
				t.Fatal("late flush changed settled result")
			}
			state, err := compaction.InspectLog(before.Events)
			if err != nil || state.Attempt != nil {
				t.Fatalf("compaction bracket remains open: %#v, %v", state, err)
			}
		})
	}
}
