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

type gatedStorage struct {
	sessionactor.Storage
	entered chan struct{}
	release chan struct{}
}

func (storage *gatedStorage) AppendBatch(ctx context.Context, batch persistence.EventBatch) error {
	storage.entered <- struct{}{}
	<-storage.release
	return storage.Storage.AppendBatch(ctx, batch)
}

func TestFlushWaitsForItsPrefixWhileMailboxRemainsAvailable(t *testing.T) {
	backend := newStorage(t)
	driver, replies := startSession(t, "creation")
	storage := &gatedStorage{
		Storage: backend,
		entered: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	// Also release the task on any early assertion failure.
	defer func() {
		select {
		case <-storage.release:
		default:
			close(storage.release)
		}
	}()
	initialized := request[sessionactor.Initialized](t, driver, replies, sessionactor.Initialize{
		SessionID:  "conversation",
		CreationID: "creation",
		Storage:    storage,
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
	for index := 0; index < 2; index++ {
		committed := request[sessionactor.Committed](t, driver, replies, sessionactor.Commit{
			CreationID: "creation",
			Plan:       session.Batch(draft),
		})
		if committed.Err != nil {
			t.Fatal(committed.Err)
		}
		if index == 0 {
			select {
			case <-storage.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("write never started")
			}
		}
	}
	if err := goakt.Tell(context.Background(), driver, command{
		payload: sessionactor.Flush{
			CreationID: "creation",
			Barrier: session.WriteBarrier{
				SessionID: "conversation",
				NextSeq:   2,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	// If Flush replies before IO finishes, this read sees the wrong response type.
	view := request[sessionactor.View](t, driver, replies, sessionactor.Read{
		CreationID: "creation",
	})
	if view.Err != nil || view.Snapshot.Barrier.NextSeq != 2 {
		t.Fatalf("read during write = %#v", view)
	}
	close(storage.release)
	select {
	case reply := <-replies:
		flushed, ok := reply.value.(sessionactor.Flushed)
		if reply.err != nil || !ok || flushed.Err != nil || flushed.Barrier.NextSeq != 2 {
			t.Fatalf("flush = %#v", reply)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("flush never completed")
	}
	stored, err := backend.Load(context.Background(), "conversation")
	if err != nil || stored == nil || len(stored.Events) != 2 {
		t.Fatalf("durable log = %#v, %v", stored, err)
	}
}

var uncertainFailure = errors.New("write committed but response failed")

type uncertainStorage struct {
	sessionactor.Storage
	calls atomic.Int32
}

func (storage *uncertainStorage) AppendBatch(ctx context.Context, batch persistence.EventBatch) error {
	storage.calls.Add(1)
	if err := storage.Storage.AppendBatch(ctx, batch); err != nil {
		return err
	}
	return uncertainFailure
}

func TestFlushReconcilesAnUncertainCommitWithoutAppendingAgain(t *testing.T) {
	backend := newStorage(t)
	driver, replies := startSession(t, "creation")
	storage := &uncertainStorage{
		Storage: backend,
	}
	initialized := request[sessionactor.Initialized](t, driver, replies, sessionactor.Initialize{
		SessionID:  "conversation",
		CreationID: "creation",
		Storage:    storage,
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
	input := sessionactor.Flush{
		CreationID: "creation",
		Barrier:    committed.Result.Barrier,
	}
	flushed := request[sessionactor.Flushed](t, driver, replies, input)
	if flushed.Err != nil {
		if !errors.Is(flushed.Err, uncertainFailure) {
			t.Fatal(flushed.Err)
		}
		flushed = request[sessionactor.Flushed](t, driver, replies, input)
	}
	if flushed.Err != nil || flushed.Barrier.NextSeq != 1 {
		t.Fatalf("retry = %#v", flushed)
	}
	if storage.calls.Load() != 1 {
		t.Fatalf("AppendBatch called %d times", storage.calls.Load())
	}
	stored, err := backend.Load(context.Background(), "conversation")
	if err != nil || stored == nil || len(stored.Events) != 1 {
		t.Fatalf("durable log = %#v, %v", stored, err)
	}
}
