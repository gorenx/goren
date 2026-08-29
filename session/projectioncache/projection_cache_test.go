package projectioncache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	sessproj "github.com/gorenx/goren/session/projection"
)

type failingOpenStore struct {
	memoryCheckpointStore
	// loadErr is returned before ProjectionCache takes ownership of this Store.
	loadErr error
}

func (store *failingOpenStore) LoadAll(
	context.Context,
) (map[session.SessionID]CheckpointRecord, error) {
	return nil, store.loadErr
}

func TestOpenFailureRollsBackForRetry(t *testing.T) {
	wanted := errors.New("load failed")
	cacheOwner, err := New(Config{
		Failures: &failureRecorder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstStore := &failingOpenStore{loadErr: wanted}
	err = cacheOwner.Open(
		context.Background(),
		&cacheLiveStore{},
		&cachePersistence{},
		sessproj.NewDriveRegistry(),
		firstStore,
	)
	if !errors.Is(err, wanted) {
		t.Fatalf("Open error = %v, want %v", err, wanted)
	}
	cacheOwner.mutex.Lock()
	rolledBack := cacheOwner.state == cacheCreated && cacheOwner.openDone == nil
	cacheOwner.mutex.Unlock()
	if !rolledBack {
		t.Fatal("failed Open did not roll back to the unopened state")
	}
	secondStore := &memoryCheckpointStore{}
	if err := cacheOwner.Open(
		context.Background(),
		&cacheLiveStore{},
		&cachePersistence{},
		sessproj.NewDriveRegistry(),
		secondStore,
	); err != nil {
		t.Fatalf("retry Open: %v", err)
	}
	if err := cacheOwner.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type blockingOpenStore struct {
	memoryCheckpointStore
	// loadEntered closes when Open starts loading durable records.
	loadEntered chan struct{}
	// releaseLoad allows the blocked durable load to complete.
	releaseLoad chan struct{}
}

func (store *blockingOpenStore) LoadAll(
	requestContext context.Context,
) (map[session.SessionID]CheckpointRecord, error) {
	close(store.loadEntered)
	select {
	case <-store.releaseLoad:
		return map[session.SessionID]CheckpointRecord{}, nil
	case <-requestContext.Done():
		return nil, context.Cause(requestContext)
	}
}

func TestCloseWaitsForOpenBeforeClosingStore(t *testing.T) {
	cacheOwner, err := New(Config{
		Failures: &failureRecorder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &blockingOpenStore{
		loadEntered: make(chan struct{}),
		releaseLoad: make(chan struct{}),
	}
	openResult := make(chan error, 1)
	go func() {
		openResult <- cacheOwner.Open(
			context.Background(),
			&cacheLiveStore{},
			&cachePersistence{},
			sessproj.NewDriveRegistry(),
			store,
		)
	}()
	select {
	case <-store.loadEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Open did not enter durable load")
	}
	closeResult := make(chan error, 1)
	go func() {
		closeResult <- cacheOwner.Close(context.Background())
	}()
	select {
	case err := <-closeResult:
		t.Fatalf("Close returned while Open was loading: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(store.releaseLoad)
	if err := <-openResult; err != nil {
		t.Fatal(err)
	}
	if err := <-closeResult; err != nil {
		t.Fatal(err)
	}
	store.mutex.Lock()
	closed := store.closed
	store.mutex.Unlock()
	if !closed {
		t.Fatal("Close did not close the Store published by Open")
	}
}
