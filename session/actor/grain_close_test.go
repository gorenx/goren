package sessionactor_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func TestCloseSealsWritesAndWaitsForActualStorageCompletion(t *testing.T) {
	backend := newStorage(t)
	driver, replies := startSession(t)
	storage := &gatedStorage{
		Storage: backend,
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	defer releaseGate(storage.release)
	initializeAndCommit(t, driver, replies, storage)
	select {
	case <-storage.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("write never started")
	}
	for index := 0; index < 2; index++ {
		if err := goakt.Tell(context.Background(), driver, command{
			payload: sessionactor.Close{
				CreationID: "creation",
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Both Close requests remain deferred while the mailbox rejects further writes.
	refused := request[sessionactor.Committed](t, driver, replies, sessionactor.Commit{
		CreationID: "creation",
		Plan:       session.Batch(),
	})
	if !errors.Is(refused.Err, session.ErrWritesClosed) {
		t.Fatalf("write during close = %#v", refused)
	}
	close(storage.release)
	for index := 0; index < 2; index++ {
		closed := receiveClosed(t, replies)
		if closed.Err != nil || closed.Barrier.NextSeq != 1 {
			t.Fatalf("close = %#v", closed)
		}
	}
	repeated := request[sessionactor.Closed](t, driver, replies, sessionactor.Close{
		CreationID: "creation",
	})
	if repeated.Err != nil || repeated.Barrier.NextSeq != 1 {
		t.Fatalf("repeated close = %#v", repeated)
	}
	reinitialized := request[sessionactor.Initialized](t, driver, replies, sessionactor.Initialize{
		CreationID: "creation",
		Storage:    storage,
	})
	if !errors.Is(reinitialized.Err, session.ErrWritesClosed) {
		t.Fatal("Initialize reopened closed Session")
	}
	stored, err := backend.Load(context.Background(), "conversation")
	if err != nil || stored == nil || len(stored.Events) != 1 {
		t.Fatalf("shared backend was closed or data missing: %#v, %v", stored, err)
	}
}

var writeFailure = errors.New("injected storage failure")

type failingStorage struct {
	sessionactor.Storage
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (storage *failingStorage) AppendBatch(ctx context.Context, batch persistence.EventBatch) error {
	if storage.calls.Add(1) == 1 {
		close(storage.entered)
		<-storage.release
		return writeFailure
	}
	return storage.Storage.AppendBatch(ctx, batch)
}

func TestFailedCloseStaysSealedAndCanRetryDurability(t *testing.T) {
	backend := newStorage(t)
	driver, replies := startSession(t)
	storage := &failingStorage{
		Storage: backend,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer releaseGate(storage.release)
	initializeAndCommit(t, driver, replies, storage)
	select {
	case <-storage.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("write never started")
	}
	if err := goakt.Tell(context.Background(), driver, command{
		payload: sessionactor.Close{
			CreationID: "creation",
		},
	}); err != nil {
		t.Fatal(err)
	}
	refused := request[sessionactor.Committed](t, driver, replies, sessionactor.Commit{
		CreationID: "creation",
		Plan:       session.Batch(),
	})
	if !errors.Is(refused.Err, session.ErrWritesClosed) {
		t.Fatal("close did not seal writes")
	}
	close(storage.release)
	failed := receiveClosed(t, replies)
	if !errors.Is(failed.Err, writeFailure) || failed.Barrier.NextSeq != 0 {
		t.Fatalf("failed close = %#v", failed)
	}
	refused = request[sessionactor.Committed](t, driver, replies, sessionactor.Commit{
		CreationID: "creation",
		Plan:       session.Batch(),
	})
	if !errors.Is(refused.Err, session.ErrWritesClosed) {
		t.Fatal("failed close reopened writes")
	}
	succeeded := request[sessionactor.Closed](t, driver, replies, sessionactor.Close{
		CreationID: "creation",
	})
	if succeeded.Err != nil || succeeded.Barrier.NextSeq != 1 {
		t.Fatalf("retry = %#v", succeeded)
	}
	if storage.calls.Load() != 2 {
		t.Fatalf("append attempts = %d", storage.calls.Load())
	}
	stored, err := backend.Load(context.Background(), "conversation")
	if err != nil || stored == nil || len(stored.Events) != 1 {
		t.Fatalf("durable log = %#v, %v", stored, err)
	}
}

func initializeAndCommit(t *testing.T, driver *goakt.PID, replies <-chan response, backend sessionactor.Storage) {
	t.Helper()
	initialized := request[sessionactor.Initialized](t, driver, replies, sessionactor.Initialize{
		CreationID: "creation",
		Storage:    backend,
	})
	if initialized.Err != nil {
		t.Fatal(initialized.Err)
	}
	draft, err := session.NewEventDraft(session.TurnStarted, session.TurnStart{
		Turn: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	committed := request[sessionactor.Committed](t, driver, replies, sessionactor.Commit{
		CreationID: "creation",
		Plan:       session.Batch(draft),
	})
	if committed.Err != nil {
		t.Fatal(committed.Err)
	}
}

func receiveClosed(t *testing.T, replies <-chan response) sessionactor.Closed {
	t.Helper()
	select {
	case reply := <-replies:
		closed, ok := reply.value.(sessionactor.Closed)
		if reply.err != nil || !ok {
			t.Fatalf("close reply = %#v", reply)
		}
		return closed
	case <-time.After(3 * time.Second):
		t.Fatal("close timed out")
		return sessionactor.Closed{}
	}
}

func releaseGate(gate chan struct{}) {
	select {
	case <-gate:
	default:
		close(gate)
	}
}
