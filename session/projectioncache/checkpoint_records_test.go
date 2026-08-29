package projectioncache

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	sessproj "github.com/gorenx/goren/session/projection"
)

func TestRecordReplacementPreservesRowsFromCurrentlyUnregisteredUnits(t *testing.T) {
	registry := sessproj.NewDriveRegistry()
	registerCountingUnit(t, registry, "active", 1)
	conversation := newCacheTestSession(t, "preserve-unregistered", 70, 1)
	store := &memoryCheckpointStore{
		loaded: map[session.SessionID]CheckpointRecord{
			conversation.ID(): {
				Identity: identityOf(conversation.Header()),
				Rows: sessproj.Checkpoint{
					"unregistered": {
						Version: 3,
						Seq:     0,
						Value:   json.RawMessage(`{"kept":true}`),
					},
				},
			},
		},
	}
	cacheOwner, _ := newCacheForTest(
		t,
		registry,
		&cachePersistence{},
		&cacheLiveStore{},
		store,
		Config{},
	)
	if err := cacheOwner.records.Replace(
		context.Background(),
		conversation.ID(),
		CheckpointRecord{
			Identity: identityOf(conversation.Header()),
			Rows: sessproj.Checkpoint{
				"active": {
					Version: 1,
					Seq:     1,
					Value:   json.RawMessage(`2`),
				},
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	store.mutex.Lock()
	stored := store.records[conversation.ID()]
	store.mutex.Unlock()
	if len(stored.Rows) != 2 ||
		string(stored.Rows["unregistered"].Value) != `{"kept":true}` {
		t.Fatalf("stored record = %#v", stored)
	}
}

func TestRecordReplacementDoesNotRegressAnExistingUnit(t *testing.T) {
	registry := sessproj.NewDriveRegistry()
	conversation := newCacheTestSession(t, "no-regression", 80, 1)
	store := &memoryCheckpointStore{
		loaded: map[session.SessionID]CheckpointRecord{
			conversation.ID(): {
				Identity: identityOf(conversation.Header()),
				Rows: sessproj.Checkpoint{
					"active": {
						Version: 1,
						Seq:     10,
						Value:   json.RawMessage(`10`),
					},
				},
			},
		},
	}
	cacheOwner, _ := newCacheForTest(
		t,
		registry,
		&cachePersistence{},
		&cacheLiveStore{},
		store,
		Config{},
	)
	if err := cacheOwner.records.Replace(
		context.Background(),
		conversation.ID(),
		CheckpointRecord{
			Identity: identityOf(conversation.Header()),
			Rows: sessproj.Checkpoint{
				"active": {
					Version: 1,
					Seq:     5,
					Value:   json.RawMessage(`5`),
				},
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	if len(store.replacementIDs()) != 0 {
		t.Fatalf("regressive replacement calls = %v", store.replacementIDs())
	}
}

func TestRecordReplacementFailureDoesNotPublishCandidate(t *testing.T) {
	wanted := errors.New("replace failed")
	identifier := session.SessionID("durable-before-memory")
	identity := LogIdentity{CreatedAt: 10}
	store := &memoryCheckpointStore{
		loaded: map[session.SessionID]CheckpointRecord{
			identifier: {
				Identity: identity,
				Rows: sessproj.Checkpoint{
					"count": {
						Version: 1,
						Seq:     1,
						Value:   json.RawMessage(`1`),
					},
				},
			},
		},
		replaceErr: wanted,
	}
	records, err := openCheckpointRecords(context.Background(), store, &failureRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	err = records.Replace(
		context.Background(),
		identifier,
		CheckpointRecord{
			Identity: identity,
			Rows: sessproj.Checkpoint{
				"count": {
					Version: 1,
					Seq:     2,
					Value:   json.RawMessage(`2`),
				},
			},
		},
	)
	if !errors.Is(err, wanted) {
		t.Fatalf("Replace error = %v, want %v", err, wanted)
	}
	record, found := records.Snapshot(identifier)
	if !found || record.Rows["count"].Seq != 1 {
		t.Fatalf("published record = %#v, found %v", record, found)
	}
}

type parallelReplacementStore struct {
	// first identifies the replacement deliberately blocked by the fixture.
	first session.SessionID
	// firstEntered closes when the first ID reaches durable Replace.
	firstEntered chan struct{}
	// releaseFirst allows the blocked first replacement to complete.
	releaseFirst chan struct{}
	// secondEntered closes when a different ID reaches durable Replace.
	secondEntered chan struct{}
	firstOnce     sync.Once
	secondOnce    sync.Once
}

func (*parallelReplacementStore) LoadAll(
	context.Context,
) (map[session.SessionID]CheckpointRecord, error) {
	return map[session.SessionID]CheckpointRecord{}, nil
}

func (store *parallelReplacementStore) Replace(
	_ context.Context,
	identifier session.SessionID,
	_ CheckpointRecord,
) error {
	if identifier == store.first {
		store.firstOnce.Do(func() { close(store.firstEntered) })
		<-store.releaseFirst
		return nil
	}
	store.secondOnce.Do(func() { close(store.secondEntered) })
	return nil
}

func (*parallelReplacementStore) Close(context.Context) error { return nil }

func TestDifferentSessionIDsReplaceInParallel(t *testing.T) {
	store := &parallelReplacementStore{
		first:         "first",
		firstEntered:  make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondEntered: make(chan struct{}),
	}
	records, err := openCheckpointRecords(context.Background(), store, &failureRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() {
		results <- records.Replace(
			context.Background(),
			"first",
			CheckpointRecord{Identity: LogIdentity{CreatedAt: 1}},
		)
	}()
	select {
	case <-store.firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first replacement did not enter durable Store")
	}
	go func() {
		results <- records.Replace(
			context.Background(),
			"second",
			CheckpointRecord{Identity: LogIdentity{CreatedAt: 2}},
		)
	}()
	select {
	case <-store.secondEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("different Session ID was serialized behind the first")
	}
	close(store.releaseFirst)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}
