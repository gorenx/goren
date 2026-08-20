package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/plugin"
)

type failureRecorder struct {
	mu       sync.Mutex
	failures []error
}

func (recorder *failureRecorder) ReportEventFailure(
	_ context.Context,
	failure plugin.EventFailure,
) {
	recorder.mu.Lock()
	recorder.failures = append(recorder.failures, failure.Error)
	recorder.mu.Unlock()
}

func (recorder *failureRecorder) ReportPostCommitFailure(failure PostCommitFailure) {
	recorder.mu.Lock()
	recorder.failures = append(recorder.failures, failure.Error)
	recorder.mu.Unlock()
}

func (recorder *failureRecorder) recordedFailures() []error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]error(nil), recorder.failures...)
}

type storeObserverPlugin struct {
	plugin.Base
	name       string
	store      LiveStore
	onCreated  func(context.Context, *Session) error
	onDisposed func(context.Context, *Session) error
	onAppended func(context.Context, *Session, Event) error
	onFlush    func(context.Context, *Session) error
}

func (observer *storeObserverPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: observer.name,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[LiveStore](),
		},
		Events: []plugin.EventSubscription{
			plugin.EventOf[SessionCreated](),
			plugin.EventOf[SessionDisposed](),
			plugin.EventOf[SessionEventAppended](),
			plugin.EventOf[SessionFlushRequested](),
		},
	}
}

func (observer *storeObserverPlugin) Apply(context.Context) error {
	store, err := plugin.Require[LiveStore](observer)
	if err != nil {
		return err
	}
	observer.store = store
	return nil
}

func (*storeObserverPlugin) Dispose(context.Context) error {
	return nil
}

func (observer *storeObserverPlugin) ObserveEvent(
	requestContext context.Context,
	fact plugin.Event,
) error {
	switch observed := fact.(type) {
	case SessionCreated:
		if observer.onCreated != nil {
			return observer.onCreated(requestContext, observed.Conversation)
		}
	case SessionDisposed:
		if observer.onDisposed != nil {
			return observer.onDisposed(requestContext, observed.Conversation)
		}
	case SessionEventAppended:
		if observer.onAppended != nil {
			return observer.onAppended(
				requestContext,
				observed.Conversation,
				observed.Committed,
			)
		}
	case SessionFlushRequested:
		if observer.onFlush != nil {
			return observer.onFlush(requestContext, observed.Conversation)
		}
	}
	return nil
}

func newStoreFixture(
	testingContext *testing.T,
	recorder *failureRecorder,
	observer *storeObserverPlugin,
) *MemoryStore {
	testingContext.Helper()
	store, err := NewMemoryStore(
		MemoryStoreOptions{
			PostCommitFailures: recorder,
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	runtimeEngine := plugin.NewRuntime(
		plugin.RuntimeSettings{
			EventFailures: recorder,
		},
	)
	if _, err := runtimeEngine.Start(
		context.Background(),
		observer,
		store,
	); err != nil {
		testingContext.Fatal(err)
	}
	testingContext.Cleanup(func() {
		if err := runtimeEngine.Shutdown(context.Background()); err != nil {
			testingContext.Error(err)
		}
	})
	return store
}

func TestStoreLifecyclePublishesCommittedEventsAndFlush(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	recorder := &failureRecorder{}
	calls := []string{}
	observer := &storeObserverPlugin{
		name: "fixture-session-observer",
		onCreated: func(context.Context, *Session) error {
			calls = append(calls, "created")
			return nil
		},
		onAppended: func(_ context.Context, activeSession *Session, committed Event) error {
			if activeSession.Seq() != committed.Seq+1 {
				return errors.New("event was published before commit")
			}
			calls = append(calls, "event")
			return nil
		},
		onFlush: func(context.Context, *Session) error {
			calls = append(calls, "flush")
			return nil
		},
		onDisposed: func(context.Context, *Session) error {
			calls = append(calls, "disposed")
			return nil
		},
	}
	store := newStoreFixture(t, recorder, observer)
	handle, err := store.Create(requestContext, nil, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversation := handle.Session()
	if _, err := Append(
		conversation,
		fixtureEventKey,
		fixturePayload{
			Items: []string{"value"},
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(requestContext, conversation); err != nil {
		t.Fatal(err)
	}
	if err := handle.Release(requestContext); err != nil {
		t.Fatal(err)
	}
	if _, found := store.Get(conversation.ID()); found {
		t.Fatal("Session remained live after handle release")
	}
	if want := []string{"created", "event", "flush", "disposed"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if failures := recorder.recordedFailures(); len(failures) != 0 {
		t.Fatalf("failures = %#v", failures)
	}
}

func TestCreationFailureRollsBackWithPairedDisposal(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	recorder := &failureRecorder{}
	sentinel := errors.New("creation veto")
	calls := []string{}
	observer := &storeObserverPlugin{
		name: "fixture-veto-observer",
		onCreated: func(context.Context, *Session) error {
			calls = append(calls, "created")
			return sentinel
		},
		onDisposed: func(context.Context, *Session) error {
			calls = append(calls, "disposed")
			return nil
		},
	}
	store := newStoreFixture(t, recorder, observer)
	identifier := SessionID("vetoed")
	if _, err := store.Create(
		requestContext,
		&identifier,
		CreateOptions{},
	); !errors.Is(err, sentinel) {
		t.Fatalf("create error = %v", err)
	}
	if _, found := store.Get(identifier); found {
		t.Fatal("vetoed Session remained in Store")
	}
	if want := []string{"created", "disposed"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestAppendObserverFailureIsContainedAndReentryRejected(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	recorder := &failureRecorder{}
	observer := &storeObserverPlugin{
		name: "fixture-reentrant-observer",
		onAppended: func(_ context.Context, activeSession *Session, _ Event) error {
			_, appendErr := Append(
				activeSession,
				fixtureEventKey,
				fixturePayload{
					Items: []string{"nested"},
				},
			)
			if appendErr == nil || !strings.Contains(appendErr.Error(), "cannot reenter") {
				return errors.New("nested append was not rejected")
			}
			return errors.New("observer failure")
		},
	}
	store := newStoreFixture(t, recorder, observer)
	handle, err := store.Create(requestContext, nil, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversation := handle.Session()
	committed, err := Append(
		conversation,
		fixtureEventKey,
		fixturePayload{
			Items: []string{"outer"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Seq != 0 || conversation.Seq() != 1 {
		t.Fatalf("committed seq = %d, next seq = %d", committed.Seq, conversation.Seq())
	}
	failures := recorder.recordedFailures()
	if len(failures) != 1 || !strings.Contains(failures[0].Error(), "observer failure") {
		t.Fatalf("observer failures = %#v", failures)
	}
}

func TestSerializedAppendsWaitForActivePublication(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	recorder := &failureRecorder{}
	publicationStarted := make(chan struct{})
	releasePublication := make(chan struct{})
	var firstPublication sync.Once
	observer := &storeObserverPlugin{
		name: "fixture-serialized-observer",
		onAppended: func(context.Context, *Session, Event) error {
			firstPublication.Do(func() {
				close(publicationStarted)
				<-releasePublication
			})
			return nil
		},
	}
	store := newStoreFixture(t, recorder, observer)
	handle, err := store.Create(requestContext, nil, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversation := handle.Session()

	firstDone := make(chan struct{})
	var firstCommitted Event
	var firstErr error
	go func() {
		firstCommitted, firstErr = AppendSerialized(
			conversation,
			fixtureEventKey,
			fixturePayload{
				Items: []string{"first"},
			},
		)
		close(firstDone)
	}()
	<-publicationStarted

	secondDone := make(chan struct{})
	var secondCommitted Event
	var secondErr error
	go func() {
		secondCommitted, secondErr = AppendSerialized(
			conversation,
			fixtureEventKey,
			fixturePayload{
				Items: []string{"second"},
			},
		)
		close(secondDone)
	}()
	select {
	case <-secondDone:
		t.Fatalf("second producer returned before active publication completed: %v", secondErr)
	case <-time.After(50 * time.Millisecond):
	}

	close(releasePublication)
	<-firstDone
	<-secondDone
	if firstErr != nil || secondErr != nil {
		t.Fatalf("serialized append errors = (%v, %v)", firstErr, secondErr)
	}
	if firstCommitted.Seq != 0 || secondCommitted.Seq != 1 || conversation.Seq() != 2 {
		t.Fatalf(
			"serialized seqs = (%d, %d), next seq = %d",
			firstCommitted.Seq,
			secondCommitted.Seq,
			conversation.Seq(),
		)
	}
}
