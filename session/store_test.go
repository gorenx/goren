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

type panickingPostCommitReporter struct{}

func (panickingPostCommitReporter) ReportPostCommitFailure(PostCommitFailure) {
	panic("fixture reporter panic")
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
	onCreated  func(context.Context, Context) error
	onDisposed func(context.Context, Context) error
	onAppended func(context.Context, Context, Event) error
	onFlush    func(context.Context, Context) error
}

func (observer *storeObserverPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: observer.name,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[LiveStore](),
		},
		Events: []plugin.EventSubscription{
			plugin.EventOf[Created](),
			plugin.EventOf[Disposed](),
			plugin.EventOf[EventAppended](),
			plugin.EventOf[FlushRequested](),
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
	case Created:
		if observer.onCreated != nil {
			return observer.onCreated(requestContext, observed.Conversation)
		}
	case Disposed:
		if observer.onDisposed != nil {
			return observer.onDisposed(requestContext, observed.Conversation)
		}
	case EventAppended:
		if observer.onAppended != nil {
			return observer.onAppended(
				requestContext,
				observed.Conversation,
				observed.Committed,
			)
		}
	case FlushRequested:
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
		onCreated: func(context.Context, Context) error {
			calls = append(calls, "created")
			return nil
		},
		onAppended: func(_ context.Context, activeSession Context, committed Event) error {
			if activeSession.Seq() != committed.Seq+1 {
				return errors.New("event was published before commit")
			}
			calls = append(calls, "event")
			return nil
		},
		onFlush: func(context.Context, Context) error {
			calls = append(calls, "flush")
			return nil
		},
		onDisposed: func(context.Context, Context) error {
			calls = append(calls, "disposed")
			return nil
		},
	}
	store := newStoreFixture(t, recorder, observer)
	membership, err := store.Create(requestContext, nil, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversation := membership.Session()
	if _, err := commitFixtureEvent(context.Background(), conversation, "value"); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(requestContext, conversation); err != nil {
		t.Fatal(err)
	}
	if err := membership.Release(requestContext); err != nil {
		t.Fatal(err)
	}
	if _, found := store.Get(conversation.ID()); found {
		t.Fatal("Session remained live after membership release")
	}
	if want := []string{"created", "event", "flush", "flush", "disposed"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if failures := recorder.recordedFailures(); len(failures) != 0 {
		t.Fatalf("failures = %#v", failures)
	}
}

func TestConcurrentReleaseSharesOneFinalFlush(t *testing.T) {
	t.Parallel()
	recorder := &failureRecorder{}
	flushStarted := make(chan struct{})
	releaseFlush := make(chan struct{})
	var flushOnce sync.Once
	flushCount := 0
	var flushMutex sync.Mutex
	observer := &storeObserverPlugin{
		name: "fixture-concurrent-release-observer",
		onFlush: func(context.Context, Context) error {
			flushMutex.Lock()
			flushCount++
			flushMutex.Unlock()
			flushOnce.Do(func() {
				close(flushStarted)
			})
			<-releaseFlush
			return nil
		},
	}
	store := newStoreFixture(t, recorder, observer)
	membership, err := store.Create(context.Background(), nil, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- membership.Release(context.Background())
	}()
	<-flushStarted

	waitContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := membership.Release(waitContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent release error = %v", err)
	}
	flushMutex.Lock()
	observedFlushes := flushCount
	flushMutex.Unlock()
	if observedFlushes != 1 {
		t.Fatalf("concurrent releases started %d final flushes", observedFlushes)
	}

	close(releaseFlush)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := membership.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	flushMutex.Lock()
	observedFlushes = flushCount
	flushMutex.Unlock()
	if observedFlushes != 1 {
		t.Fatalf("repeated release started %d final flushes", observedFlushes)
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
		onCreated: func(context.Context, Context) error {
			calls = append(calls, "created")
			return sentinel
		},
		onDisposed: func(context.Context, Context) error {
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

func TestCreationRollbackDetachesWhenFinalFlushFails(t *testing.T) {
	t.Parallel()
	requestContext, cancel := context.WithCancel(context.Background())
	recorder := &failureRecorder{}
	creationFailure := errors.New("creation veto")
	flushFailure := errors.New("rollback flush failed")
	observer := &storeObserverPlugin{
		name: "fixture-rollback-observer",
		onCreated: func(context.Context, Context) error {
			cancel()
			return creationFailure
		},
		onFlush: func(context.Context, Context) error {
			return flushFailure
		},
	}
	store := newStoreFixture(t, recorder, observer)
	identifier := SessionID("rollback-flush-failure")
	_, err := store.Create(
		requestContext,
		&identifier,
		CreateOptions{},
	)
	if !errors.Is(err, creationFailure) || !errors.Is(err, flushFailure) {
		t.Fatalf("create error = %v", err)
	}
	if _, found := store.Get(identifier); found {
		t.Fatal("vetoed Session remained in Store after rollback flush failure")
	}
}

func TestAppendObserverFailureIsContainedAndReentryRejected(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	recorder := &failureRecorder{}
	observer := &storeObserverPlugin{
		name: "fixture-reentrant-observer",
		onAppended: func(publicationContext context.Context, activeSession Context, _ Event) error {
			draft, err := NewEventDraft(
				fixtureEventKey,
				fixturePayload{
					Items: []string{"nested"},
				},
			)
			if err != nil {
				return err
			}
			_, appendErr := activeSession.Commit(publicationContext, Batch(draft))
			if !errors.Is(appendErr, ErrWriteReentry) {
				return errors.New("nested append was not rejected")
			}
			return errors.New("observer failure")
		},
	}
	store := newStoreFixture(t, recorder, observer)
	membership, err := store.Create(requestContext, nil, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversation := membership.Session()
	committed, err := commitFixtureEvent(context.Background(), conversation, "outer")
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

func TestPostCommitReporterPanicDoesNotChangeCommitResult(t *testing.T) {
	t.Parallel()
	store, err := NewMemoryStore(
		MemoryStoreOptions{
			PostCommitFailures: panickingPostCommitReporter{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := store.Prepare(nil, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	membership, err := store.Enter(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commitFixtureEvent(context.Background(), conversation, "committed"); err != nil {
		t.Fatalf("post-commit reporter panic changed Commit result: %v", err)
	}
	if conversation.Seq() != 1 {
		t.Fatalf("next seq = %d", conversation.Seq())
	}
	if err := membership.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCommitWaitsForActivePublication(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	recorder := &failureRecorder{}
	publicationStarted := make(chan struct{})
	releasePublication := make(chan struct{})
	var firstPublication sync.Once
	observer := &storeObserverPlugin{
		name: "fixture-ordered-observer",
		onAppended: func(context.Context, Context, Event) error {
			firstPublication.Do(func() {
				close(publicationStarted)
				<-releasePublication
			})
			return nil
		},
	}
	store := newStoreFixture(t, recorder, observer)
	membership, err := store.Create(requestContext, nil, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversation := membership.Session()

	firstDone := make(chan struct{})
	var firstCommitted Event
	var firstErr error
	go func() {
		firstCommitted, firstErr = commitFixtureEvent(
			context.Background(),
			conversation,
			"first",
		)
		close(firstDone)
	}()
	<-publicationStarted

	secondDone := make(chan struct{})
	var secondCommitted Event
	var secondErr error
	go func() {
		secondCommitted, secondErr = commitFixtureEvent(
			context.Background(),
			conversation,
			"second",
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
		t.Fatalf("commit errors = (%v, %v)", firstErr, secondErr)
	}
	if firstCommitted.Seq != 0 || secondCommitted.Seq != 1 || conversation.Seq() != 2 {
		t.Fatalf(
			"committed seqs = (%d, %d), next seq = %d",
			firstCommitted.Seq,
			secondCommitted.Seq,
			conversation.Seq(),
		)
	}
}
