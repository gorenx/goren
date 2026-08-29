package projectioncache

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/session"
	sessproj "github.com/gorenx/goren/session/projection"
)

func TestCachedSnapshotUsesIdentityCompatibleRowsAndMinimumCut(t *testing.T) {
	registry := sessproj.NewDriveRegistry()
	registerCountingUnit(t, registry, "first", 1)
	registerCountingUnit(t, registry, "second", 1)
	conversation := newCacheTestSession(t, "cached", 10, 3)
	rows := checkpointAt(t, registry, conversation, 2)
	second := rows["second"]
	second.Seq = 0
	rows["second"] = second
	store := &memoryCheckpointStore{
		loaded: map[session.SessionID]CheckpointRecord{
			conversation.ID(): {
				Identity: identityOf(conversation.Header()),
				Rows:     rows,
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
	projectionSnapshot, err := cacheOwner.CachedSnapshot(conversation.Header())
	if err != nil || projectionSnapshot == nil {
		t.Fatalf("CachedSnapshot = (%#v, %v)", projectionSnapshot, err)
	}
	if projectionSnapshot.AsOfSeq != 0 || string(projectionSnapshot.Values["first"]) != "2" ||
		string(projectionSnapshot.Values["second"]) != "2" {
		t.Fatalf("snapshot = %#v", projectionSnapshot)
	}
	different := conversation.Header()
	different.CreatedAt++
	if projectionSnapshot, err := cacheOwner.CachedSnapshot(different); err != nil || projectionSnapshot != nil {
		t.Fatalf("mismatched identity = (%#v, %v), want absent", projectionSnapshot, err)
	}
}

func TestColdSnapshotRestoresOnlyCheckpointSuffix(t *testing.T) {
	registry := sessproj.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	conversation := newCacheTestSession(t, "suffix", 20, 4)
	rows := checkpointAt(t, registry, conversation, 2)
	store := &memoryCheckpointStore{
		loaded: map[session.SessionID]CheckpointRecord{
			conversation.ID(): {
				Identity: identityOf(conversation.Header()),
				Rows:     rows,
			},
		},
	}
	source := &cachePersistence{
		header: conversation.Header(),
		events: conversation.Events(),
	}
	cacheOwner, _ := newCacheForTest(
		t,
		registry,
		source,
		&cacheLiveStore{},
		store,
		Config{},
	)
	projectionSnapshot, err := cacheOwner.ColdSnapshot(context.Background(), conversation.ID())
	if err != nil {
		t.Fatal(err)
	}
	if projectionSnapshot.AsOfSeq != 4 || string(projectionSnapshot.Values["count"]) != "5" {
		t.Fatalf("snapshot = %#v", projectionSnapshot)
	}
	readCalls, windowCalls := source.observations()
	if !reflect.DeepEqual(readCalls, []int64{1}) || windowCalls != 0 {
		t.Fatalf("reads = %v, windows = %d", readCalls, windowCalls)
	}
	if len(store.replacementIDs()) != 1 {
		t.Fatalf("write-back calls = %v", store.replacementIDs())
	}
}

func TestColdSnapshotFoldsMultipleEventPages(t *testing.T) {
	registry := sessproj.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	conversation := newCacheTestSession(t, "paged-restore", 25, 1025)
	source := &cachePersistence{
		header: conversation.Header(),
		events: conversation.Events(),
	}
	cacheOwner, _ := newCacheForTest(
		t,
		registry,
		source,
		&cacheLiveStore{},
		&memoryCheckpointStore{},
		Config{},
	)
	projectionSnapshot, err := cacheOwner.ColdSnapshot(context.Background(), conversation.ID())
	if err != nil {
		t.Fatal(err)
	}
	if projectionSnapshot.AsOfSeq != 1025 || string(projectionSnapshot.Values["count"]) != "1026" {
		t.Fatalf("snapshot = %#v", projectionSnapshot)
	}
	readCalls, windowCalls := source.observations()
	if !reflect.DeepEqual(readCalls, []int64{0, 512, 1024}) || windowCalls != 0 {
		t.Fatalf("reads = %v, windows = %d", readCalls, windowCalls)
	}
}

func TestColdSnapshotFallsBackAfterShrunkLog(t *testing.T) {
	registry := sessproj.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	conversation := newCacheTestSession(t, "shrunk", 30, 4)
	rows := checkpointAt(t, registry, conversation, 3)
	shortEvents := conversation.Events()[:2]
	source := &cachePersistence{
		header: conversation.Header(),
		events: append([]session.Event(nil), shortEvents...),
	}
	store := &memoryCheckpointStore{
		loaded: map[session.SessionID]CheckpointRecord{
			conversation.ID(): {
				Identity: identityOf(conversation.Header()),
				Rows:     rows,
			},
		},
	}
	cacheOwner, _ := newCacheForTest(
		t,
		registry,
		source,
		&cacheLiveStore{},
		store,
		Config{},
	)
	projectionSnapshot, err := cacheOwner.ColdSnapshot(context.Background(), conversation.ID())
	if err != nil {
		t.Fatal(err)
	}
	if projectionSnapshot.AsOfSeq != 1 || string(projectionSnapshot.Values["count"]) != "2" {
		t.Fatalf("snapshot = %#v", projectionSnapshot)
	}
	readCalls, _ := source.observations()
	if !reflect.DeepEqual(readCalls, []int64{2, 0}) {
		t.Fatalf("ReadFrom calls = %v", readCalls)
	}
}

func TestColdSnapshotWithNoUnitsProbesOnlyLatestEvent(t *testing.T) {
	registry := sessproj.NewDriveRegistry()
	conversation := newCacheTestSession(t, "empty-values", 40, 3)
	source := &cachePersistence{
		header: conversation.Header(),
		events: conversation.Events(),
	}
	cacheOwner, _ := newCacheForTest(
		t,
		registry,
		source,
		&cacheLiveStore{},
		&memoryCheckpointStore{},
		Config{},
	)
	projectionSnapshot, err := cacheOwner.ColdSnapshot(context.Background(), conversation.ID())
	if err != nil {
		t.Fatal(err)
	}
	if projectionSnapshot.AsOfSeq != 3 || len(projectionSnapshot.Values) != 0 {
		t.Fatalf("snapshot = %#v", projectionSnapshot)
	}
	readCalls, windowCalls := source.observations()
	if len(readCalls) != 0 || windowCalls != 1 {
		t.Fatalf("reads = %v, windows = %d", readCalls, windowCalls)
	}
}

func TestColdSnapshotWriteBackFailureIsContained(t *testing.T) {
	registry := sessproj.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	conversation := newCacheTestSession(t, "write-back", 50, 2)
	store := &memoryCheckpointStore{
		replaceErr: errors.New("disk full"),
	}
	cacheOwner, failures := newCacheForTest(
		t,
		registry,
		&cachePersistence{
			header: conversation.Header(),
			events: conversation.Events(),
		},
		&cacheLiveStore{},
		store,
		Config{},
	)
	projectionSnapshot, err := cacheOwner.ColdSnapshot(context.Background(), conversation.ID())
	if err != nil || projectionSnapshot.AsOfSeq != 2 {
		t.Fatalf("ColdSnapshot = (%#v, %v)", projectionSnapshot, err)
	}
	reported := failures.recordedFailures()
	if len(reported) != 1 || reported[0].Operation != "cold checkpoint write-back" {
		t.Fatalf("failures = %#v", reported)
	}
}

func TestColdSnapshotReturnsAuthoritativeReadFailure(t *testing.T) {
	registry := sessproj.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	wanted := errors.New("read failed")
	cacheOwner, _ := newCacheForTest(
		t,
		registry,
		&cachePersistence{
			readErr: wanted,
		},
		&cacheLiveStore{},
		&memoryCheckpointStore{},
		Config{},
	)
	_, err := cacheOwner.ColdSnapshot(context.Background(), "missing")
	if !errors.Is(err, wanted) {
		t.Fatalf("error = %v, want %v", err, wanted)
	}
}

func TestOpenDropsMalformedCheckpointRecord(t *testing.T) {
	registry := sessproj.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	conversation := newCacheTestSession(t, "malformed", 60, 1)
	store := &memoryCheckpointStore{
		loaded: map[session.SessionID]CheckpointRecord{
			conversation.ID(): {
				Identity: identityOf(conversation.Header()),
				Rows: sessproj.Checkpoint{
					"count": {
						Version: 1,
						Seq:     0,
						Value:   json.RawMessage(`{`),
					},
				},
			},
		},
	}
	source := &cachePersistence{
		header: conversation.Header(),
		events: conversation.Events(),
	}
	cacheOwner, failures := newCacheForTest(
		t,
		registry,
		source,
		&cacheLiveStore{},
		store,
		Config{},
	)
	if projectionSnapshot, err := cacheOwner.CachedSnapshot(conversation.Header()); err != nil || projectionSnapshot != nil {
		t.Fatalf("CachedSnapshot = (%#v, %v), want absent", projectionSnapshot, err)
	}
	if len(failures.recordedFailures()) != 1 {
		t.Fatalf("failures = %#v", failures.recordedFailures())
	}
}
