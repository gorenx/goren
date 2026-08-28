package projectioncache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

func TestLiveCheckpointFlushesBeforeStoreReplace(t *testing.T) {
	registry := sessionprojection.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	conversation := newCacheTestSession(t, "write-order", 70, 1)
	flushEntered := make(chan struct{}, 1)
	flushRelease := make(chan struct{})
	live := &cacheLiveStore{
		conversation: conversation,
		flushEntered: flushEntered,
		flushRelease: flushRelease,
	}
	store := &memoryCheckpointStore{
		replaced: make(chan session.SessionID, 1),
	}
	cacheOwner, _ := newCacheForTest(
		t,
		registry,
		&cachePersistence{},
		live,
		store,
		Config{
			WriteEveryEvents: 1,
		},
	)
	if err := cacheOwner.Advance(conversation, conversation.Events()[0]); err != nil {
		t.Fatal(err)
	}
	select {
	case <-flushEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("checkpoint did not enter Flush")
	}
	if storedIDs := store.replacementIDs(); len(storedIDs) != 0 {
		t.Fatalf("Store.Replace ran before Flush completed: %v", storedIDs)
	}
	close(flushRelease)
	if identifier := waitForSignal(t, store.replaced); identifier != conversation.ID() {
		t.Fatalf("checkpoint Session = %q", identifier)
	}
}

func TestRetireWritesFinalCheckpointForSessionWithoutObservedEvents(t *testing.T) {
	registry := sessionprojection.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	conversation := newCacheTestSession(t, "retire", 80, 2)
	store := &memoryCheckpointStore{
		replaced: make(chan session.SessionID, 1),
	}
	cacheOwner, _ := newCacheForTest(
		t,
		registry,
		&cachePersistence{},
		&cacheLiveStore{},
		store,
		Config{},
	)
	if err := cacheOwner.Retire(conversation); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, store.replaced)
	store.mutex.Lock()
	record := store.records[conversation.ID()]
	store.mutex.Unlock()
	if record.Rows["count"].Seq != 2 {
		t.Fatalf("checkpoint = %#v", record)
	}
}

func TestProjectionCacheRetireOrdering(t *testing.T) {
	registry := sessionprojection.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	conversation := newCacheTestSession(t, "retire-during-writer", 85, 1)
	flushEntered := make(chan struct{}, 1)
	flushRelease := make(chan struct{})
	live := &cacheLiveStore{
		conversation: conversation,
		flushEntered: flushEntered,
		flushRelease: flushRelease,
	}
	store := &memoryCheckpointStore{
		replaceErrors: []error{errors.New("active writer failed"), nil},
		replaced:      make(chan session.SessionID, 1),
	}
	cacheOwner, failures := newCacheForTest(
		t,
		registry,
		&cachePersistence{},
		live,
		store,
		Config{WriteEveryEvents: 1},
	)
	if err := cacheOwner.Advance(conversation, conversation.Events()[0]); err != nil {
		t.Fatal(err)
	}
	select {
	case <-flushEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("active writer did not enter Flush")
	}
	if err := cacheOwner.Retire(conversation); err != nil {
		t.Fatal(err)
	}
	close(flushRelease)
	if identifier := waitForSignal(t, store.replaced); identifier != conversation.ID() {
		t.Fatalf("final checkpoint Session = %q", identifier)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		cacheOwner.mutex.Lock()
		checkpoint := cacheOwner.checkpoints[conversation.ID()]
		cacheOwner.mutex.Unlock()
		checkpoint.writersMutex.Lock()
		_, retained := checkpoint.writers[conversation]
		checkpoint.writersMutex.Unlock()
		if !retained {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retired writer state was not removed")
		}
		time.Sleep(time.Millisecond)
	}
	if calls := store.replacementIDs(); len(calls) != 2 {
		t.Fatalf("checkpoint attempts = %v, want active plus one final", calls)
	}
	if reported := failures.recordedFailures(); len(reported) != 1 ||
		reported[0].Error.Error() != "active writer failed" {
		t.Fatalf("failures = %#v", reported)
	}
	getCalls, flushCalls := live.observations()
	if getCalls != 0 || flushCalls != 1 {
		t.Fatalf("LiveStore calls = Get %d, Flush %d", getCalls, flushCalls)
	}
}

func TestReusedSessionIDRejectsOlderLifecycleWriter(t *testing.T) {
	registry := sessionprojection.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	older := newCacheTestSession(t, "reused-writer", 85, 1)
	newer := newCacheTestSession(t, "reused-writer", 86, 1)
	flushEntered := make(chan struct{}, 1)
	flushRelease := make(chan struct{})
	live := &cacheLiveStore{
		flushEntered: flushEntered,
		flushRelease: flushRelease,
	}
	store := &memoryCheckpointStore{
		replaced: make(chan session.SessionID, 1),
	}
	cacheOwner, _ := newCacheForTest(
		t,
		registry,
		&cachePersistence{},
		live,
		store,
		Config{WriteEveryEvents: 1},
	)
	if err := cacheOwner.Begin(older); err != nil {
		t.Fatal(err)
	}
	if err := cacheOwner.Advance(older, older.Events()[0]); err != nil {
		t.Fatal(err)
	}
	select {
	case <-flushEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("older lifecycle writer did not enter Flush")
	}
	if err := cacheOwner.Begin(newer); err != nil {
		t.Fatal(err)
	}
	close(flushRelease)
	if err := cacheOwner.Advance(newer, newer.Events()[0]); err != nil {
		t.Fatal(err)
	}
	if identifier := waitForSignal(t, store.replaced); identifier != newer.ID() {
		t.Fatalf("checkpoint Session = %q", identifier)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		store.mutex.Lock()
		record := store.records[newer.ID()]
		calls := len(store.replaceCalls)
		store.mutex.Unlock()
		if calls == 1 && record.Identity.CreatedAt == newer.Header().CreatedAt {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("record = %#v, calls = %d", record, calls)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLiveCheckpointFailureKeepsDirtyStateForLaterTrigger(t *testing.T) {
	registry := sessionprojection.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	conversation := newCacheTestSession(t, "retry", 90, 1)
	store := &memoryCheckpointStore{
		replaceErr: errors.New("temporary"),
	}
	cacheOwner, failures := newCacheForTest(
		t,
		registry,
		&cachePersistence{},
		&cacheLiveStore{},
		store,
		Config{
			WriteEveryEvents: 1,
			WriteInterval:    time.Hour,
		},
	)
	if err := cacheOwner.Advance(conversation, conversation.Events()[0]); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(failures.recordedFailures()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(failures.recordedFailures()) != 1 {
		t.Fatalf("failures = %#v", failures.recordedFailures())
	}
	cacheOwner.mutex.Lock()
	checkpoint := cacheOwner.checkpoints[conversation.ID()]
	cacheOwner.mutex.Unlock()
	checkpoint.writersMutex.Lock()
	writer := checkpoint.writers[conversation]
	checkpoint.writersMutex.Unlock()
	writer.mutex.Lock()
	dirtyEvents := writer.pendingEvents()
	writing := writer.schedule.isWriting()
	writer.mutex.Unlock()
	if dirtyEvents != 1 || writing {
		t.Fatalf("state pending=%d writing=%v", dirtyEvents, writing)
	}
}

func TestCloseWaitsForEnteredCheckpointAndRejectsNewColdReads(t *testing.T) {
	registry := sessionprojection.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	conversation := newCacheTestSession(t, "close", 100, 1)
	flushEntered := make(chan struct{}, 1)
	flushRelease := make(chan struct{})
	store := &memoryCheckpointStore{}
	cacheOwner, err := New(Config{
		WriteEveryEvents: 1,
		Failures:         &failureRecorder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cacheOwner.Open(
		context.Background(),
		&cacheLiveStore{
			conversation: conversation,
			flushEntered: flushEntered,
			flushRelease: flushRelease,
		},
		&cachePersistence{},
		registry,
		store,
	); err != nil {
		t.Fatal(err)
	}
	if err := cacheOwner.Advance(conversation, conversation.Events()[0]); err != nil {
		t.Fatal(err)
	}
	select {
	case <-flushEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("checkpoint did not enter Flush")
	}
	closed := make(chan error, 1)
	go func() {
		closed <- cacheOwner.Close(context.Background())
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, coldErr := cacheOwner.ColdSnapshot(context.Background(), conversation.ID())
		if errors.Is(coldErr, ErrClosed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ColdSnapshot error = %v, want ErrClosed", coldErr)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case closeErr := <-closed:
		t.Fatalf("Close returned before Flush release: %v", closeErr)
	default:
	}
	close(flushRelease)
	select {
	case closeErr := <-closed:
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not finish")
	}
	store.mutex.Lock()
	closedStore := store.closed
	store.mutex.Unlock()
	if !closedStore {
		t.Fatal("checkpoint store was not closed")
	}
}

func TestCheckpointCutRejectsRowsFromDifferentEvents(t *testing.T) {
	_, err := checkpointCut(
		sessionprojection.Checkpoint{
			"first": {
				Seq:   1,
				Value: []byte(`null`),
			},
			"second": {
				Seq:   2,
				Value: []byte(`null`),
			},
		},
		-1,
	)
	if err == nil {
		t.Fatal("checkpointCut accepted rows from different cuts")
	}
}
