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

type blockingFlushPublisher struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type scriptedSessionPublisher struct {
	mutex   sync.Mutex
	flushes []WriteBarrier
	onFlush func(int, FlushRequested) error
}

func (*scriptedSessionPublisher) Created(context.Context, Context) error {
	return nil
}

func (*scriptedSessionPublisher) Appended(context.Context, Context, Event) {}

func (*scriptedSessionPublisher) Disposed(context.Context, Context) {}

func (publisher *scriptedSessionPublisher) Flush(
	_ context.Context,
	conversation Context,
	committedPrefix WriteBarrier,
) error {
	flushRequest := FlushRequested{
		Conversation: conversation,
		Barrier:      committedPrefix,
	}
	publisher.mutex.Lock()
	publisher.flushes = append(publisher.flushes, flushRequest.Barrier)
	index := len(publisher.flushes)
	handler := publisher.onFlush
	publisher.mutex.Unlock()
	if handler == nil {
		return nil
	}
	return handler(index, flushRequest)
}

func (publisher *scriptedSessionPublisher) observedFlushes() []WriteBarrier {
	publisher.mutex.Lock()
	defer publisher.mutex.Unlock()
	return append([]WriteBarrier(nil), publisher.flushes...)
}

func (*blockingFlushPublisher) Created(context.Context, Context) error {
	return nil
}

func (*blockingFlushPublisher) Appended(context.Context, Context, Event) {}

func (*blockingFlushPublisher) Disposed(context.Context, Context) {}

func (publisher *blockingFlushPublisher) Flush(
	_ context.Context,
	_ Context,
	_ WriteBarrier,
) error {
	publisher.once.Do(func() {
		close(publisher.started)
	})
	<-publisher.release
	return nil
}

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
) *memoryStore {
	testingContext.Helper()
	storePlugin, err := NewPlugin(
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
		storePlugin,
	); err != nil {
		testingContext.Fatal(err)
	}
	testingContext.Cleanup(func() {
		if err := runtimeEngine.Shutdown(context.Background()); err != nil {
			testingContext.Error(err)
		}
	})
	return storePlugin.store
}

func newDirectStore(
	testingContext *testing.T,
	publisher eventPublisher,
) *memoryStore {
	testingContext.Helper()
	store, err := newMemoryStore(
		nil,
		publisher,
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return store
}

func waitForTerminalAdmission(
	testingContext *testing.T,
	owner *sessionContext,
) {
	testingContext.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		terminal := owner.terminalAdmission()
		if terminal {
			return
		}
		if time.Now().After(deadline) {
			testingContext.Fatal("Release did not acquire terminal admission")
		}
		time.Sleep(time.Millisecond)
	}
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
	finalFlushCount := flushCount
	flushMutex.Unlock()
	if finalFlushCount != 1 {
		t.Fatalf("concurrent releases started %d final flushes", finalFlushCount)
	}

	close(releaseFlush)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := membership.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	flushMutex.Lock()
	repeatedFlushCount := flushCount
	flushMutex.Unlock()
	if repeatedFlushCount != 1 {
		t.Fatalf("repeated release started %d final flushes", repeatedFlushCount)
	}
}

func TestReleaseWaitsForAnnouncementState(t *testing.T) {
	t.Parallel()
	recorder := &failureRecorder{}
	announcementStarted := make(chan struct{})
	finishAnnouncement := make(chan struct{})
	observer := &storeObserverPlugin{
		name: "fixture-announcement-state-observer",
		onCreated: func(context.Context, Context) error {
			close(announcementStarted)
			<-finishAnnouncement
			return nil
		},
	}
	store := newStoreFixture(t, recorder, observer)
	conversation, err := store.Prepare(nil, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	membership, err := store.Enter(conversation)
	if err != nil {
		t.Fatal(err)
	}
	announcementDone := make(chan error, 1)
	go func() {
		announcementDone <- store.Announce(context.Background(), conversation)
	}()
	<-announcementStarted
	releaseDone := make(chan error, 1)
	go func() {
		releaseDone <- membership.Release(context.Background())
	}()
	select {
	case err = <-releaseDone:
		t.Fatalf("Release returned before announcement completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(finishAnnouncement)
	if err = <-announcementDone; err != nil {
		t.Fatal(err)
	}
	if err = <-releaseDone; err != nil {
		t.Fatal(err)
	}
	if _, found := store.Get(conversation.ID()); found {
		t.Fatal("released Session remained live")
	}
}

func TestStoreCloseRejectsPreparationAndEntryBeforeDrain(t *testing.T) {
	t.Parallel()
	publisher := &blockingFlushPublisher{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	store, err := newMemoryStore(
		nil,
		publisher,
	)
	if err != nil {
		t.Fatal(err)
	}
	membership, err := store.Create(
		context.Background(),
		nil,
		CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	detached, err := New("detached-before-close", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- store.Close(context.Background())
	}()
	<-publisher.started
	if _, err = store.Prepare(nil, CreateOptions{}); err == nil ||
		!strings.Contains(err.Error(), "not accepting") {
		t.Fatalf("Prepare during Close error = %v", err)
	}
	if _, err = store.Enter(detached); err == nil ||
		!strings.Contains(err.Error(), "not accepting") {
		t.Fatalf("Enter during Close error = %v", err)
	}
	if _, err = store.Create(
		context.Background(),
		nil,
		CreateOptions{},
	); err == nil || !strings.Contains(err.Error(), "not accepting") {
		t.Fatalf("Create during Close error = %v", err)
	}
	close(publisher.release)
	if err = <-closeDone; err != nil {
		t.Fatal(err)
	}
	if _, found := store.Get(membership.Session().ID()); found {
		t.Fatal("Store Close retained a Session")
	}
	if _, err = store.Prepare(nil, CreateOptions{}); err == nil ||
		!strings.Contains(err.Error(), "not accepting") {
		t.Fatalf("Prepare after Close error = %v", err)
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

func TestCreationVetoFlushFailureKeepsSealedSessionForRetry(t *testing.T) {
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
		t.Fatal("sealed Session remained visible after rollback flush failure")
	}
	store.mutex.RLock()
	sealed := store.sessions[identifier]
	store.mutex.RUnlock()
	if sealed == nil {
		t.Fatal("failed final flush discarded the exact cleanup owner")
	}
	state := sealed.conversation.currentPhase()
	if state != sessionSealed {
		t.Fatalf("vetoed Session state = %d, want sealed", state)
	}
}

func TestSessionReleaseOrdering(t *testing.T) {
	t.Run("flush before release", func(t *testing.T) {
		flushStarted := make(chan struct{})
		allowFlush := make(chan struct{})
		publisher := &scriptedSessionPublisher{
			onFlush: func(index int, _ FlushRequested) error {
				if index == 1 {
					close(flushStarted)
					<-allowFlush
				}
				return nil
			},
		}
		store := newDirectStore(t, publisher)
		handleState, err := store.Create(context.Background(), nil, CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		conversation := handleState.Session()
		if _, err := commitFixtureEvent(context.Background(), conversation, "before-flush"); err != nil {
			t.Fatal(err)
		}
		flushDone := make(chan error, 1)
		go func() {
			flushDone <- store.Flush(context.Background(), conversation)
		}()
		<-flushStarted
		releaseDone := make(chan error, 1)
		go func() {
			releaseDone <- handleState.Release(context.Background())
		}()
		waitForTerminalAdmission(t, conversation.(*sessionContext))
		if _, err := conversation.Commit(
			context.Background(),
			Batch(newFixtureDraft(t, "rejected")),
		); !errors.Is(err, ErrWritesClosed) {
			t.Fatalf("Commit after terminal admission error = %v", err)
		}
		close(allowFlush)
		if err := <-flushDone; err != nil {
			t.Fatal(err)
		}
		if err := <-releaseDone; err != nil {
			t.Fatal(err)
		}
		flushes := publisher.observedFlushes()
		if len(flushes) != 2 || flushes[0].NextSeq != 1 || flushes[1].NextSeq != 1 {
			t.Fatalf("flush barriers = %#v", flushes)
		}
	})

	t.Run("release before flush", func(t *testing.T) {
		flushStarted := make(chan struct{})
		allowFlush := make(chan struct{})
		publisher := &scriptedSessionPublisher{
			onFlush: func(index int, _ FlushRequested) error {
				if index == 1 {
					close(flushStarted)
					<-allowFlush
				}
				return nil
			},
		}
		store := newDirectStore(t, publisher)
		handleState, err := store.Create(context.Background(), nil, CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		conversation := handleState.Session()
		releaseDone := make(chan error, 1)
		go func() {
			releaseDone <- handleState.Release(context.Background())
		}()
		<-flushStarted
		if err := store.Flush(context.Background(), conversation); !errors.Is(err, ErrWritesClosed) {
			t.Fatalf("Flush after terminal admission error = %v", err)
		}
		close(allowFlush)
		if err := <-releaseDone; err != nil {
			t.Fatal(err)
		}
		if flushes := publisher.observedFlushes(); len(flushes) != 1 {
			t.Fatalf("release started %d final flushes", len(flushes))
		}
	})

	t.Run("canceled waiter does not cancel finalization", func(t *testing.T) {
		flushStarted := make(chan struct{})
		allowFlush := make(chan struct{})
		publisher := &scriptedSessionPublisher{
			onFlush: func(index int, _ FlushRequested) error {
				if index == 1 {
					close(flushStarted)
					<-allowFlush
				}
				return nil
			},
		}
		store := newDirectStore(t, publisher)
		handleState, err := store.Create(context.Background(), nil, CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		waitContext, cancel := context.WithCancel(context.Background())
		firstDone := make(chan error, 1)
		go func() {
			firstDone <- handleState.Release(waitContext)
		}()
		<-flushStarted
		cancel()
		if err := <-firstDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled Release waiter error = %v", err)
		}
		secondDone := make(chan error, 1)
		go func() {
			secondDone <- handleState.Release(context.Background())
		}()
		if flushes := publisher.observedFlushes(); len(flushes) != 1 {
			t.Fatalf("concurrent waiter started %d final flushes", len(flushes))
		}
		close(allowFlush)
		if err := <-secondDone; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("failed final flush remains sealed and retries", func(t *testing.T) {
		flushFailure := errors.New("final flush failed")
		publisher := &scriptedSessionPublisher{
			onFlush: func(index int, _ FlushRequested) error {
				if index == 1 {
					return flushFailure
				}
				return nil
			},
		}
		store := newDirectStore(t, publisher)
		handleState, err := store.Create(context.Background(), nil, CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		conversation := handleState.Session()
		if err := handleState.Release(context.Background()); !errors.Is(err, flushFailure) {
			t.Fatalf("first Release error = %v", err)
		}
		if _, found := store.Get(conversation.ID()); found {
			t.Fatal("sealed Session remained business-visible")
		}
		if _, err := conversation.Commit(
			context.Background(),
			Batch(newFixtureDraft(t, "rejected")),
		); !errors.Is(err, ErrWritesClosed) {
			t.Fatalf("sealed Commit error = %v", err)
		}
		if err := handleState.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
		if flushes := publisher.observedFlushes(); len(flushes) != 2 {
			t.Fatalf("retry started %d final flushes", len(flushes))
		}
	})

	t.Run("old handle cannot affect reused id", func(t *testing.T) {
		publisher := &scriptedSessionPublisher{}
		store := newDirectStore(t, publisher)
		identifier := SessionID("reused")
		oldHandle, err := store.Create(context.Background(), &identifier, CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := oldHandle.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
		newHandle, err := store.Create(context.Background(), &identifier, CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := oldHandle.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
		current, found := store.Get(identifier)
		if !found || current != newHandle.Session() {
			t.Fatal("old Handle affected the replacement lifecycle")
		}
		if err := newHandle.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAppendObserverFailureIsContainedAndReentryRejected(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	recorder := &failureRecorder{}
	observer := &storeObserverPlugin{
		name: "fixture-reentrant-observer",
		onAppended: func(eventContext context.Context, activeSession Context, _ Event) error {
			draft, err := NewEventDraft(
				fixtureEventKey,
				fixturePayload{
					Items: []string{"nested"},
				},
			)
			if err != nil {
				return err
			}
			_, appendErr := activeSession.Commit(eventContext, Batch(draft))
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
	storePlugin, err := NewPlugin(
		MemoryStoreOptions{
			PostCommitFailures: panickingPostCommitReporter{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	store := storePlugin.store
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
