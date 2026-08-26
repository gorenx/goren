package projectioncache

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sessproj "github.com/gorenx/goren/session/projection"
)

const cacheTestEventName = "test/session-projection-cache"

var cacheTestEvent = session.DefineEvent[struct {
	Value int `json:"value"`
}](cacheTestEventName)

type countingUnit struct {
	key     string
	version int64
	changed bool
}

func (unit countingUnit) Key() string { return unit.key }

func (unit countingUnit) StateVersion() int64 { return unit.version }

func (countingUnit) InitialState() (json.RawMessage, error) {
	return json.RawMessage(`0`), nil
}

func (unit countingUnit) ApplyState(
	state json.RawMessage,
	_ session.Event,
) (sessproj.Transition, error) {
	var count int
	if err := json.Unmarshal(state, &count); err != nil {
		return sessproj.Transition{}, err
	}
	next, err := json.Marshal(count + 1)
	return sessproj.Transition{
		State:   next,
		Changed: unit.changed,
	}, err
}

func (countingUnit) ViewState(state json.RawMessage) (json.RawMessage, error) {
	return append(json.RawMessage(nil), state...), nil
}

type failureRecorder struct {
	mutex    sync.Mutex
	failures []Failure
}

func (recorder *failureRecorder) ReportProjectionCacheFailure(reported Failure) {
	recorder.mutex.Lock()
	recorder.failures = append(recorder.failures, reported)
	recorder.mutex.Unlock()
}

func (recorder *failureRecorder) recordedFailures() []Failure {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	return append([]Failure(nil), recorder.failures...)
}

type memoryCheckpointStore struct {
	mutex        sync.Mutex
	loaded       map[session.SessionID]CheckpointRecord
	records      map[session.SessionID]CheckpointRecord
	replaceErr   error
	replaceCalls []session.SessionID
	replaced     chan session.SessionID
	closed       bool
}

func (store *memoryCheckpointStore) LoadAll(
	context.Context,
) (map[session.SessionID]CheckpointRecord, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	result := make(map[session.SessionID]CheckpointRecord, len(store.loaded))
	for identifier, record := range store.loaded {
		result[identifier] = record
	}
	return result, nil
}

func (store *memoryCheckpointStore) Replace(
	_ context.Context,
	identifier session.SessionID,
	record CheckpointRecord,
) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.replaceCalls = append(store.replaceCalls, identifier)
	if store.replaceErr != nil {
		return store.replaceErr
	}
	if store.records == nil {
		store.records = make(map[session.SessionID]CheckpointRecord)
	}
	store.records[identifier] = cloneRecord(record)
	if store.replaced != nil {
		select {
		case store.replaced <- identifier:
		default:
		}
	}
	return nil
}

func (store *memoryCheckpointStore) Close(context.Context) error {
	store.mutex.Lock()
	store.closed = true
	store.mutex.Unlock()
	return nil
}

func (store *memoryCheckpointStore) replacementIDs() []session.SessionID {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	return append([]session.SessionID(nil), store.replaceCalls...)
}

type cachePersistence struct {
	sesspersist.Persistence
	mutex       sync.Mutex
	header      session.Header
	events      []session.Event
	readCalls   []int64
	windowCalls int
	readErr     error
}

func (source *cachePersistence) ReadEventsFrom(
	_ context.Context,
	_ session.SessionID,
	continuation sesspersist.EventContinuation,
) (sesspersist.EventSegment, error) {
	source.mutex.Lock()
	readErr := source.readErr
	header := source.header
	events := append([]session.Event(nil), source.events...)
	source.mutex.Unlock()
	if readErr != nil {
		return sesspersist.EventSegment{}, readErr
	}
	fromSeq := continuation.FromSeq
	source.mutex.Lock()
	source.readCalls = append(source.readCalls, fromSeq)
	source.mutex.Unlock()
	if fromSeq >= int64(len(events)) {
		events = nil
	} else {
		events = events[fromSeq:]
	}
	hasMore := int64(len(events)) > continuation.Limit
	if hasMore {
		events = events[:continuation.Limit]
	}
	return sesspersist.EventSegment{
		Header:   header,
		Revision: "test-revision",
		Events:   events,
		HasMore:  hasMore,
	}, nil
}

func (source *cachePersistence) observations() ([]int64, int) {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	return append([]int64(nil), source.readCalls...), source.windowCalls
}

func (source *cachePersistence) ReadEventsBefore(
	_ context.Context,
	_ session.SessionID,
	_ sesspersist.EventPage,
) (sesspersist.EventWindow, error) {
	source.mutex.Lock()
	source.windowCalls++
	header := source.header
	events := append([]session.Event(nil), source.events...)
	readErr := source.readErr
	source.mutex.Unlock()
	if readErr != nil {
		return sesspersist.EventWindow{}, readErr
	}
	if len(events) > 1 {
		events = events[len(events)-1:]
	}
	if len(events) == 1 {
		events = []session.Event{events[0]}
	}
	return sesspersist.EventWindow{
		Header: header,
		Events: events,
	}, nil
}

type cacheLiveStore struct {
	session.LiveStore
	mutex        sync.Mutex
	conversation session.Context
	flushEntered chan struct{}
	flushRelease <-chan struct{}
	flushCalls   int
}

func (store *cacheLiveStore) Get(
	identifier session.SessionID,
) (session.Context, bool) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.conversation == nil || store.conversation.ID() != identifier {
		return nil, false
	}
	return store.conversation, true
}

func (store *cacheLiveStore) Flush(
	requestContext context.Context,
	conversation session.Context,
) error {
	store.mutex.Lock()
	store.flushCalls++
	entered := store.flushEntered
	release := store.flushRelease
	store.mutex.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release == nil {
		return nil
	}
	select {
	case <-release:
		return nil
	case <-requestContext.Done():
		return context.Cause(requestContext)
	}
}

func newCacheTestSession(
	t *testing.T,
	identifier session.SessionID,
	createdAt int64,
	eventCount int,
) session.Context {
	t.Helper()
	events := make([]session.Event, eventCount)
	for index := range eventCount {
		payload, err := json.Marshal(struct {
			Value int `json:"value"`
		}{
			Value: index,
		})
		if err != nil {
			t.Fatal(err)
		}
		events[index] = session.Event{
			Type: cacheTestEventName,
			Seq:  int64(index),
			Time: int64(index + 1),
			Data: payload,
		}
	}
	conversation, err := session.New(
		identifier,
		session.CreateOptions{
			Seed: events,
			Metadata: session.Metadata{
				CreatedAt: &createdAt,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return conversation
}

func newCacheForTest(
	t *testing.T,
	registry *sessproj.DriveRegistry,
	source *cachePersistence,
	live *cacheLiveStore,
	store *memoryCheckpointStore,
	settings Config,
) (*CheckpointCache, *failureRecorder) {
	t.Helper()
	failures := &failureRecorder{}
	settings.Failures = failures
	cacheOwner, err := New(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := cacheOwner.Open(
		context.Background(),
		live,
		source,
		registry,
		store,
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cacheOwner.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return cacheOwner, failures
}

func registerCountingUnit(
	t *testing.T,
	registry *sessproj.DriveRegistry,
	projectionKey string,
	version int64,
) {
	t.Helper()
	if _, err := registry.Register(countingUnit{
		key:     projectionKey,
		version: version,
		changed: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func checkpointAt(
	t *testing.T,
	registry *sessproj.DriveRegistry,
	conversation session.Context,
	endExclusive int,
) sessproj.Checkpoint {
	t.Helper()
	result, err := registry.Restore(
		nil,
		conversation.Events()[:endExclusive],
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	return result.Checkpoint
}

func waitForSignal(t *testing.T, signal <-chan session.SessionID) session.SessionID {
	t.Helper()
	select {
	case identifier := <-signal:
		return identifier
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for checkpoint write")
		return ""
	}
}
