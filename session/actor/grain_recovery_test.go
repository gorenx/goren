package sessionactor_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence"
)

func TestResumeRepairsInterruptedSessionAndPreservesSeedBoundary(t *testing.T) {
	backend := newStorage(t)
	seed := interruptedLog(t)
	if err := backend.AppendBatch(context.Background(), persistence.EventBatch{
		Header:      seed.Header(),
		Events:      seed.Events(),
		Materialize: true,
	}); err != nil {
		t.Fatal(err)
	}
	driver, replies := startSession(t, "resumed")
	resumed := request[sessionactor.Initialized](t, driver, replies, sessionactor.Resume{
		CreationID: "resumed",
		Storage:    backend,
	})
	if resumed.Err != nil || resumed.Header.CreatedAt != seed.Header().CreatedAt || resumed.Barrier.NextSeq != 5 {
		t.Fatalf("resume = %#v", resumed)
	}
	view := request[sessionactor.View](t, driver, replies, sessionactor.Read{
		CreationID: "resumed",
	})
	expected := []string{session.TurnStartEventName, session.StepStartEventName, session.StepEndEventName, session.TurnEndEventName, session.EndSeedEventName}
	if view.Err != nil || len(view.Snapshot.Events) != len(expected) {
		t.Fatalf("view = %#v", view)
	}
	for index, name := range expected {
		if view.Snapshot.Events[index].Type != name || view.Snapshot.Events[index].Seq != int64(index) {
			t.Fatalf("event %d = %#v", index, view.Snapshot.Events[index])
		}
	}
	repeated := request[sessionactor.Initialized](t, driver, replies, sessionactor.Resume{
		CreationID: "resumed",
		Storage:    backend,
	})
	if repeated.Err != nil || repeated.Barrier.NextSeq != 5 {
		t.Fatalf("duplicate resume = %#v", repeated)
	}
	closed := request[sessionactor.Closed](t, driver, replies, sessionactor.Close{
		CreationID: "resumed",
	})
	if closed.Err != nil || closed.Barrier.NextSeq != 5 {
		t.Fatalf("close = %#v", closed)
	}
	stored, err := backend.Load(context.Background(), "conversation")
	if err != nil || stored == nil || len(stored.Events) != 5 {
		t.Fatalf("stored = %#v, %v", stored, err)
	}
}

var repairFailure = errors.New("repair failed")

type failingRepair struct {
	sessionactor.RecoveryStorage
	calls atomic.Int32
}

func (storage *failingRepair) CommitRepair(ctx context.Context, repair persistence.LogRepair) error {
	if storage.calls.Add(1) == 1 {
		return repairFailure
	}
	return storage.RecoveryStorage.CommitRepair(ctx, repair)
}

func TestResumeFailureDoesNotInstallPartialSession(t *testing.T) {
	backend := newStorage(t)
	driver, replies := startSession(t, "resumed")
	absent := request[sessionactor.Initialized](t, driver, replies, sessionactor.Resume{
		CreationID: "resumed",
		Storage:    backend,
	})
	var missing *persistence.NotFoundError
	if !errors.As(absent.Err, &missing) {
		t.Fatalf("missing log = %#v", absent)
	}
	seed := interruptedLog(t)
	if err := backend.AppendBatch(context.Background(), persistence.EventBatch{
		Header:      seed.Header(),
		Events:      seed.Events(),
		Materialize: true,
	}); err != nil {
		t.Fatal(err)
	}
	storage := &failingRepair{
		RecoveryStorage: backend,
	}
	failed := request[sessionactor.Initialized](t, driver, replies, sessionactor.Resume{
		CreationID: "resumed",
		Storage:    storage,
	})
	if !errors.Is(failed.Err, repairFailure) {
		t.Fatalf("repair failure = %#v", failed)
	}
	unavailable := request[sessionactor.View](t, driver, replies, sessionactor.Read{
		CreationID: "resumed",
	})
	if !errors.Is(unavailable.Err, sessionactor.ErrUnavailable) {
		t.Fatal("failed recovery installed a partial log")
	}
	retried := request[sessionactor.Initialized](t, driver, replies, sessionactor.Resume{
		CreationID: "resumed",
		Storage:    storage,
	})
	if retried.Err != nil || retried.Barrier.NextSeq != 5 {
		t.Fatalf("retry = %#v", retried)
	}
	closed := request[sessionactor.Closed](t, driver, replies, sessionactor.Close{
		CreationID: "resumed",
	})
	if closed.Err != nil {
		t.Fatal(closed.Err)
	}
	if storage.calls.Load() != 2 {
		t.Fatalf("repair attempts = %d", storage.calls.Load())
	}
}

func interruptedLog(t *testing.T) *session.Log {
	t.Helper()
	history, err := session.NewLog("conversation", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	started, err := session.NewEventDraft(session.TurnStarted, session.TurnStart{
		Turn: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	step, err := session.NewEventDraft(session.StepStarted, session.StepPosition{
		Turn: 1,
		Step: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(context.Background(), session.Batch(started, step)); err != nil {
		t.Fatal(err)
	}
	return history
}
