package projectioncache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

func TestLiveCheckpointTriggers(t *testing.T) {
	t.Run("event count", func(t *testing.T) {
		registry := sessionprojection.NewDriveRegistry()
		registerCountingUnit(t, registry, "count", 1)
		conversation := newCacheTestSession(t, "count-trigger", 60, 1)
		store := &memoryCheckpointStore{
			replaced: make(chan session.SessionID, 1),
		}
		cacheOwner, _ := newCacheForTest(
			t,
			registry,
			&cachePersistence{},
			&cacheLiveStore{},
			store,
			Config{
				WriteEveryEvents: 3,
				WriteInterval:    time.Hour,
			},
		)
		for range 2 {
			if err := cacheOwner.EventAppended(
				conversation,
				conversation.Events()[0],
			); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case identifier := <-store.replaced:
			t.Fatalf("checkpoint triggered early for %q", identifier)
		case <-time.After(20 * time.Millisecond):
		}
		if err := cacheOwner.EventAppended(
			conversation,
			conversation.Events()[0],
		); err != nil {
			t.Fatal(err)
		}
		waitForSignal(t, store.replaced)
	})

	t.Run("interval", func(t *testing.T) {
		registry := sessionprojection.NewDriveRegistry()
		registerCountingUnit(t, registry, "count", 1)
		conversation := newCacheTestSession(t, "interval-trigger", 61, 1)
		store := &memoryCheckpointStore{
			replaced: make(chan session.SessionID, 1),
		}
		cacheOwner, _ := newCacheForTest(
			t,
			registry,
			&cachePersistence{},
			&cacheLiveStore{},
			store,
			Config{
				WriteEveryEvents: 100,
				WriteInterval:    time.Millisecond,
			},
		)
		if err := cacheOwner.EventAppended(
			conversation,
			conversation.Events()[0],
		); err != nil {
			t.Fatal(err)
		}
		waitForSignal(t, store.replaced)
	})

	t.Run("turn end", func(t *testing.T) {
		registry := sessionprojection.NewDriveRegistry()
		registerCountingUnit(t, registry, "count", 1)
		conversation := newCacheTestSession(t, "turn-end-trigger", 62, 1)
		store := &memoryCheckpointStore{
			replaced: make(chan session.SessionID, 1),
		}
		cacheOwner, _ := newCacheForTest(
			t,
			registry,
			&cachePersistence{},
			&cacheLiveStore{},
			store,
			Config{
				WriteEveryEvents: 100,
				WriteInterval:    time.Hour,
			},
		)
		if err := cacheOwner.EventAppended(
			conversation,
			session.Event{Type: session.TurnEndEventName},
		); err != nil {
			t.Fatal(err)
		}
		waitForSignal(t, store.replaced)
	})
}

func TestLiveCheckpointFlushesBeforeStoreReplace(t *testing.T) {
	registry := sessionprojection.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	conversation := newCacheTestSession(t, "lifecycle-order", 70, 1)
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
	if err := cacheOwner.EventAppended(conversation, conversation.Events()[0]); err != nil {
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

func TestSameSessionCheckpointAttemptsAreSerial(t *testing.T) {
	registry := sessionprojection.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	conversation := newCacheTestSession(t, "serial-lifecycle", 71, 1)
	flushEntered := make(chan struct{}, 2)
	flushRelease := make(chan struct{})
	live := &cacheLiveStore{
		conversation: conversation,
		flushEntered: flushEntered,
		flushRelease: flushRelease,
	}
	store := &memoryCheckpointStore{
		replaced: make(chan session.SessionID, 2),
	}
	cacheOwner, _ := newCacheForTest(
		t,
		registry,
		&cachePersistence{},
		live,
		store,
		Config{
			WriteEveryEvents: 1,
			WriteInterval:    time.Hour,
		},
	)
	if err := cacheOwner.EventAppended(conversation, conversation.Events()[0]); err != nil {
		t.Fatal(err)
	}
	waitForCheckpointEntry(t, flushEntered)
	if err := cacheOwner.EventAppended(conversation, conversation.Events()[0]); err != nil {
		t.Fatal(err)
	}
	select {
	case <-flushEntered:
		t.Fatal("second checkpoint entered Flush before the first completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(flushRelease)
	waitForSignal(t, store.replaced)
	waitForCheckpointEntry(t, flushEntered)
	_, flushCalls := live.observations()
	if flushCalls != 2 {
		t.Fatalf("Flush calls = %d, want two serial attempts", flushCalls)
	}
}

func TestDifferentSessionCheckpointAttemptsRunInParallel(t *testing.T) {
	registry := sessionprojection.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	first := newCacheTestSession(t, "parallel-first", 72, 1)
	second := newCacheTestSession(t, "parallel-second", 73, 1)
	flushEntered := make(chan struct{}, 2)
	flushRelease := make(chan struct{})
	live := &cacheLiveStore{
		flushEntered: flushEntered,
		flushRelease: flushRelease,
	}
	store := &memoryCheckpointStore{
		replaced: make(chan session.SessionID, 2),
	}
	cacheOwner, _ := newCacheForTest(
		t,
		registry,
		&cachePersistence{},
		live,
		store,
		Config{
			WriteEveryEvents: 1,
			WriteInterval:    time.Hour,
		},
	)
	if err := cacheOwner.EventAppended(first, first.Events()[0]); err != nil {
		t.Fatal(err)
	}
	if err := cacheOwner.EventAppended(second, second.Events()[0]); err != nil {
		t.Fatal(err)
	}
	waitForCheckpointEntry(t, flushEntered)
	waitForCheckpointEntry(t, flushEntered)
	close(flushRelease)
	firstSeen := false
	secondSeen := false
	for range 2 {
		switch waitForSignal(t, store.replaced) {
		case first.ID():
			firstSeen = true
		case second.ID():
			secondSeen = true
		}
	}
	if !firstSeen || !secondSeen {
		t.Fatalf("checkpointed Sessions = (%v, %v)", firstSeen, secondSeen)
	}
}

func TestSessionDisposedWritesFinalCheckpointWithoutObservedEvents(t *testing.T) {
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
	if err := cacheOwner.SessionDisposed(conversation); err != nil {
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

func TestSessionDisposedFollowsActiveCheckpoint(t *testing.T) {
	registry := sessionprojection.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	conversation := newCacheTestSession(t, "retire-during-lifecycle", 85, 1)
	flushEntered := make(chan struct{}, 1)
	flushRelease := make(chan struct{})
	live := &cacheLiveStore{
		conversation: conversation,
		flushEntered: flushEntered,
		flushRelease: flushRelease,
	}
	store := &memoryCheckpointStore{
		replaceErrors: []error{errors.New("active lifecycle failed"), nil},
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
	if err := cacheOwner.EventAppended(conversation, conversation.Events()[0]); err != nil {
		t.Fatal(err)
	}
	select {
	case <-flushEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("active lifecycle did not enter Flush")
	}
	if err := cacheOwner.SessionDisposed(conversation); err != nil {
		t.Fatal(err)
	}
	close(flushRelease)
	if identifier := waitForSignal(t, store.replaced); identifier != conversation.ID() {
		t.Fatalf("final checkpoint Session = %q", identifier)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		cacheOwner.live.mutex.Lock()
		_, retained := cacheOwner.live.lifecycles[conversation]
		cacheOwner.live.mutex.Unlock()
		if !retained {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retired lifecycle state was not removed")
		}
		time.Sleep(time.Millisecond)
	}
	if calls := store.replacementIDs(); len(calls) != 2 {
		t.Fatalf("checkpoint attempts = %v, want active plus one final", calls)
	}
	if reported := failures.recordedFailures(); len(reported) != 1 ||
		reported[0].Error.Error() != "active lifecycle failed" {
		t.Fatalf("failures = %#v", reported)
	}
	getCalls, flushCalls := live.observations()
	if getCalls != 0 || flushCalls != 1 {
		t.Fatalf("LiveStore calls = Get %d, Flush %d", getCalls, flushCalls)
	}
}

func TestDetachedLiveCheckpointDefersToFinalCheckpoint(t *testing.T) {
	registry := sessionprojection.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	conversation := newCacheTestSession(t, "detached-during-lifecycle", 86, 1)
	flushEntered := make(chan struct{}, 1)
	flushRelease := make(chan struct{})
	live := &cacheLiveStore{
		conversation: conversation,
		flushEntered: flushEntered,
		flushRelease: flushRelease,
		flushErr:     session.ErrNotAttached,
	}
	store := &memoryCheckpointStore{
		replaced: make(chan session.SessionID, 1),
	}
	cacheOwner, failures := newCacheForTest(
		t,
		registry,
		&cachePersistence{},
		live,
		store,
		Config{
			WriteEveryEvents: 1,
			WriteInterval:    time.Hour,
		},
	)
	if err := cacheOwner.EventAppended(conversation, conversation.Events()[0]); err != nil {
		t.Fatal(err)
	}
	select {
	case <-flushEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("live checkpoint did not attempt Flush")
	}
	if err := cacheOwner.SessionDisposed(conversation); err != nil {
		t.Fatal(err)
	}
	close(flushRelease)
	if identifier := waitForSignal(t, store.replaced); identifier != conversation.ID() {
		t.Fatalf("final checkpoint Session = %q", identifier)
	}
	if reported := failures.recordedFailures(); len(reported) != 0 {
		t.Fatalf("superseded live checkpoint failures = %#v", reported)
	}
	_, flushCalls := live.observations()
	if flushCalls != 1 {
		t.Fatalf("Flush calls = %d, want one superseded live attempt", flushCalls)
	}
}

func TestUnavailableLiveCheckpointWaitsForSessionDisposed(t *testing.T) {
	registry := sessionprojection.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	conversation := newCacheTestSession(t, "detached-before-disposal", 87, 1)
	live := &cacheLiveStore{
		conversation: conversation,
		flushErr:     session.ErrNotAttached,
	}
	store := &memoryCheckpointStore{
		replaced: make(chan session.SessionID, 1),
	}
	cacheOwner, failures := newCacheForTest(
		t,
		registry,
		&cachePersistence{},
		live,
		store,
		Config{
			WriteEveryEvents: 1,
			WriteInterval:    time.Hour,
		},
	)
	if err := cacheOwner.EventAppended(conversation, conversation.Events()[0]); err != nil {
		t.Fatal(err)
	}
	waitForCheckpointLifecycleState(t, cacheOwner, conversation, checkpointLifecycleDelayed)
	select {
	case identifier := <-store.replaced:
		t.Fatalf("unavailable live attempt wrote record for %q", identifier)
	default:
	}
	if reported := failures.recordedFailures(); len(reported) != 0 {
		t.Fatalf("unavailable live attempt failures = %#v", reported)
	}
	if err := cacheOwner.SessionDisposed(conversation); err != nil {
		t.Fatal(err)
	}
	if identifier := waitForSignal(t, store.replaced); identifier != conversation.ID() {
		t.Fatalf("final checkpoint Session = %q", identifier)
	}
	_, flushCalls := live.observations()
	if flushCalls != 1 {
		t.Fatalf("Flush calls = %d, want no immediate unavailable retry", flushCalls)
	}
}

func TestCheckpointResultClassification(t *testing.T) {
	checkpointErr := errors.New("checkpoint failed")
	tests := []struct {
		name        string
		attempt     checkpointAttempt
		err         error
		wantOutcome checkpointOutcome
		wantFailure bool
	}{
		{
			name: "success",
			attempt: checkpointAttempt{
				id:   1,
				kind: liveCheckpoint,
			},
			wantOutcome: checkpointSucceeded,
		},
		{
			name: "live membership unavailable",
			attempt: checkpointAttempt{
				id:   1,
				kind: liveCheckpoint,
			},
			err:         session.ErrNotAttached,
			wantOutcome: checkpointUnavailable,
		},
		{
			name: "final failure is reportable",
			attempt: checkpointAttempt{
				id:   1,
				kind: finalCheckpoint,
			},
			err:         session.ErrNotAttached,
			wantOutcome: checkpointFailed,
			wantFailure: true,
		},
		{
			name: "ordinary failure",
			attempt: checkpointAttempt{
				id:   1,
				kind: liveCheckpoint,
			},
			err:         checkpointErr,
			wantOutcome: checkpointFailed,
			wantFailure: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := classifyCheckpointResult(test.attempt, test.err)
			if result.outcome != test.wantOutcome ||
				(result.failure != nil) != test.wantFailure {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestDifferentLogIdentityUsesLastCompletedReplacement(t *testing.T) {
	registry := sessionprojection.NewDriveRegistry()
	registerCountingUnit(t, registry, "count", 1)
	older := newCacheTestSession(t, "reused-lifecycle", 85, 1)
	newer := newCacheTestSession(t, "reused-lifecycle", 86, 1)
	store := &memoryCheckpointStore{
		replaced: make(chan session.SessionID, 2),
	}
	cacheOwner, _ := newCacheForTest(
		t,
		registry,
		&cachePersistence{},
		&cacheLiveStore{},
		store,
		Config{WriteEveryEvents: 1},
	)
	if err := cacheOwner.SessionDisposed(older); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, store.replaced)
	if err := cacheOwner.SessionDisposed(newer); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, store.replaced)
	if projectionSnapshot, err := cacheOwner.CachedSnapshot(older.Header()); err != nil || projectionSnapshot != nil {
		t.Fatalf("older identity snapshot = (%#v, %v), want miss", projectionSnapshot, err)
	}
	if projectionSnapshot, err := cacheOwner.CachedSnapshot(newer.Header()); err != nil || projectionSnapshot == nil {
		t.Fatalf("newer identity snapshot = (%#v, %v), want hit", projectionSnapshot, err)
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
	if err := cacheOwner.EventAppended(conversation, conversation.Events()[0]); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(failures.recordedFailures()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(failures.recordedFailures()) != 1 {
		t.Fatalf("failures = %#v", failures.recordedFailures())
	}
	cacheOwner.live.mutex.Lock()
	lifecycle := cacheOwner.live.lifecycles[conversation]
	cacheOwner.live.mutex.Unlock()
	dirtyEvents, writing := inspectCheckpointLifecycle(lifecycle)
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
	if err := cacheOwner.EventAppended(conversation, conversation.Events()[0]); err != nil {
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

func TestValidateCheckpointCutRejectsRowsFromDifferentEvents(t *testing.T) {
	err := validateCheckpointCut(
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
	)
	if err == nil {
		t.Fatal("validateCheckpointCut accepted rows from different cuts")
	}
}

func inspectCheckpointLifecycle(lifecycle *checkpointLifecycle) (uint64, bool) {
	lifecycle.mutex.Lock()
	defer lifecycle.mutex.Unlock()
	writing := lifecycle.state == checkpointLifecycleRunning ||
		lifecycle.state == checkpointLifecycleQueued ||
		lifecycle.state == checkpointLifecycleFinalQueued ||
		lifecycle.state == checkpointLifecycleFinalRunning
	return lifecycle.pendingEvents(), writing
}

func waitForCheckpointLifecycleState(
	t *testing.T,
	cacheOwner *ProjectionCache,
	conversation session.Context,
	want checkpointLifecycleState,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		cacheOwner.live.mutex.Lock()
		lifecycle := cacheOwner.live.lifecycles[conversation]
		cacheOwner.live.mutex.Unlock()
		if lifecycle != nil {
			lifecycle.mutex.Lock()
			state := lifecycle.state
			lifecycle.mutex.Unlock()
			if state == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("checkpoint lifecycle did not reach state %d", want)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForCheckpointEntry(
	t *testing.T,
	entered <-chan struct{},
) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("checkpoint did not enter Flush")
	}
}
