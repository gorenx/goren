package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type snapshotPlan struct {
	draft    EventDraft
	observed chan Snapshot
}

func (plan *snapshotPlan) Build(
	_ context.Context,
	currentSnapshot Snapshot,
) ([]EventDraft, error) {
	plan.observed <- currentSnapshot
	return []EventDraft{plan.draft}, nil
}

type cancellationPlan struct {
	cancel context.CancelFunc
	draft  EventDraft
}

func (plan cancellationPlan) Build(context.Context, Snapshot) ([]EventDraft, error) {
	plan.cancel()
	return []EventDraft{plan.draft}, nil
}

type panicPlan struct{}

func (panicPlan) Build(context.Context, Snapshot) ([]EventDraft, error) {
	panic("fixture panic")
}

func TestPlanBuildSeesSnapshotAtFIFOHead(t *testing.T) {
	t.Parallel()
	conversation, err := New("head-snapshot", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	owner := conversation.(*coordinator)
	firstDraft := newFixtureDraft(t, "first")
	secondDraft := newFixtureDraft(t, "second")
	entered := make(chan struct{})
	resume := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, commitErr := conversation.Commit(
			context.Background(),
			&blockingPlan{
				drafts:  []EventDraft{firstDraft},
				entered: entered,
				resume:  resume,
			},
		)
		firstDone <- commitErr
	}()
	<-entered

	observed := make(chan Snapshot, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, commitErr := conversation.Commit(
			context.Background(),
			&snapshotPlan{
				draft:    secondDraft,
				observed: observed,
			},
		)
		secondDone <- commitErr
	}()
	waitForPendingItems(t, owner, 1)
	close(resume)

	currentSnapshot := <-observed
	if len(currentSnapshot.Events) != 1 || currentSnapshot.Events[0].Seq != 0 ||
		currentSnapshot.Barrier.NextSeq != 1 {
		t.Fatalf("head Snapshot = %#v", currentSnapshot)
	}
	if commitErr := <-firstDone; commitErr != nil {
		t.Fatal(commitErr)
	}
	if commitErr := <-secondDone; commitErr != nil {
		t.Fatal(commitErr)
	}
	assertFixtureItems(t, conversation.Events(), []string{"first", "second"})
}

func TestCommitRejectsWholeBatchWhenLaterDraftCannotApply(t *testing.T) {
	t.Parallel()
	conversation, err := New("atomic-batch", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	firstDraft := newFixtureDraft(t, "must-not-commit")
	invalidDraft, err := NewSurfaceEventDraft(
		defineSurfaceEvent[fixturePayload]("user/message"),
		fixturePayload{
			Items: []string{"invalid-replacement"},
		},
		SurfaceIntent{
			Operation: SurfaceReplace(0, 0),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := conversation.Commit(
		context.Background(),
		Batch(firstDraft, invalidDraft),
	)
	if err == nil {
		t.Fatal("invalid batch committed")
	}
	if len(result.Events) != 0 || result.FirstSeq != 0 || result.NextSeq != 0 {
		t.Fatalf("failed batch result = %#v", result)
	}
	if currentSnapshot := conversation.Snapshot(); len(currentSnapshot.Events) != 0 ||
		len(currentSnapshot.Surface.Nodes) != 0 || currentSnapshot.Barrier.NextSeq != 0 {
		t.Fatalf("failed batch mutated Session = %#v", currentSnapshot)
	}
}

func TestQueuedCancellationSkipsPlanBuild(t *testing.T) {
	t.Parallel()
	conversation, err := New("queued-cancellation", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	owner := conversation.(*coordinator)
	entered := make(chan struct{})
	resume := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, commitErr := conversation.Commit(
			context.Background(),
			&blockingPlan{
				drafts:  []EventDraft{newFixtureDraft(t, "first")},
				entered: entered,
				resume:  resume,
			},
		)
		firstDone <- commitErr
	}()
	<-entered

	requestContext, cancel := context.WithCancel(context.Background())
	observed := make(chan Snapshot, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, commitErr := conversation.Commit(
			requestContext,
			&snapshotPlan{
				draft:    newFixtureDraft(t, "cancelled"),
				observed: observed,
			},
		)
		secondDone <- commitErr
	}()
	waitForPendingItems(t, owner, 1)
	cancel()
	close(resume)
	if commitErr := <-firstDone; commitErr != nil {
		t.Fatal(commitErr)
	}
	if commitErr := <-secondDone; !errors.Is(commitErr, context.Canceled) {
		t.Fatalf("queued cancellation error = %v", commitErr)
	}
	select {
	case currentSnapshot := <-observed:
		t.Fatalf("cancelled plan built with Snapshot %#v", currentSnapshot)
	default:
	}
	assertFixtureItems(t, conversation.Events(), []string{"first"})
}

func TestCancellationAfterBuildCommitsNothing(t *testing.T) {
	t.Parallel()
	conversation, err := New("build-cancellation", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancel := context.WithCancel(context.Background())
	_, err = conversation.Commit(
		requestContext,
		cancellationPlan{
			cancel: cancel,
			draft:  newFixtureDraft(t, "cancelled"),
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("post-Build cancellation error = %v", err)
	}
	if conversation.Seq() != 0 {
		t.Fatalf("post-Build cancellation committed %d Events", conversation.Seq())
	}
}

func TestPlanPanicDoesNotStopQueue(t *testing.T) {
	t.Parallel()
	conversation, err := New("plan-panic", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.Commit(context.Background(), panicPlan{}); err == nil ||
		!strings.Contains(err.Error(), "fixture panic") {
		t.Fatalf("panic error = %v", err)
	}
	if _, err := conversation.Commit(
		context.Background(),
		Batch(newFixtureDraft(t, "after-panic")),
	); err != nil {
		t.Fatal(err)
	}
	assertFixtureItems(t, conversation.Events(), []string{"after-panic"})
}

func TestSealDrainsAdmittedItemsAndRejectsNewWrites(t *testing.T) {
	t.Parallel()
	conversation, err := New("seal", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	owner := conversation.(*coordinator)
	entered := make(chan struct{})
	resume := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, commitErr := conversation.Commit(
			context.Background(),
			&blockingPlan{
				drafts:  []EventDraft{newFixtureDraft(t, "first")},
				entered: entered,
				resume:  resume,
			},
		)
		firstDone <- commitErr
	}()
	<-entered
	secondDone := make(chan error, 1)
	go func() {
		_, commitErr := conversation.Commit(
			context.Background(),
			Batch(newFixtureDraft(t, "second")),
		)
		secondDone <- commitErr
	}()
	waitForPendingItems(t, owner, 1)

	barriers := make(chan WriteBarrier, 1)
	sealErrors := make(chan error, 1)
	go func() {
		barrier, sealErr := owner.sealWrites(context.Background())
		barriers <- barrier
		sealErrors <- sealErr
	}()
	waitForQueueState(t, owner, queueSealing)
	if _, err := conversation.Commit(
		context.Background(),
		Batch(newFixtureDraft(t, "rejected")),
	); !errors.Is(err, ErrWritesClosed) {
		t.Fatalf("write after seal error = %v", err)
	}
	close(resume)
	if commitErr := <-firstDone; commitErr != nil {
		t.Fatal(commitErr)
	}
	if commitErr := <-secondDone; commitErr != nil {
		t.Fatal(commitErr)
	}
	barrier := <-barriers
	if sealErr := <-sealErrors; sealErr != nil {
		t.Fatal(sealErr)
	}
	if barrier.SessionID != conversation.ID() || barrier.NextSeq != 2 {
		t.Fatalf("seal barrier = %#v", barrier)
	}
	repeated, err := owner.sealWrites(context.Background())
	if err != nil || repeated != barrier {
		t.Fatalf("repeated seal = (%#v, %v)", repeated, err)
	}
	assertFixtureItems(t, conversation.Events(), []string{"first", "second"})
}

func TestOrderedBarrierSeparatesEarlierAndLaterWrites(t *testing.T) {
	t.Parallel()
	conversation, err := New("barrier", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	owner := conversation.(*coordinator)
	entered := make(chan struct{})
	resume := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, commitErr := conversation.Commit(
			context.Background(),
			&blockingPlan{
				drafts:  []EventDraft{newFixtureDraft(t, "before")},
				entered: entered,
				resume:  resume,
			},
		)
		firstDone <- commitErr
	}()
	<-entered

	barriers := make(chan WriteBarrier, 1)
	barrierErrors := make(chan error, 1)
	go func() {
		barrier, barrierErr := owner.orderedBarrier(context.Background())
		barriers <- barrier
		barrierErrors <- barrierErr
	}()
	waitForPendingItems(t, owner, 1)
	laterDone := make(chan error, 1)
	go func() {
		_, commitErr := conversation.Commit(
			context.Background(),
			Batch(newFixtureDraft(t, "after")),
		)
		laterDone <- commitErr
	}()
	waitForPendingItems(t, owner, 2)
	close(resume)

	barrier := <-barriers
	if barrierErr := <-barrierErrors; barrierErr != nil {
		t.Fatal(barrierErr)
	}
	if commitErr := <-firstDone; commitErr != nil {
		t.Fatal(commitErr)
	}
	if commitErr := <-laterDone; commitErr != nil {
		t.Fatal(commitErr)
	}
	if barrier.SessionID != conversation.ID() || barrier.NextSeq != 1 {
		t.Fatalf("ordered barrier = %#v", barrier)
	}
	if conversation.Seq() != 2 {
		t.Fatalf("final Seq = %d", conversation.Seq())
	}
}

func TestBatchDetachesDraftBeforeAdmission(t *testing.T) {
	t.Parallel()
	conversation, err := New("batch-snapshot", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	draft := newFixtureDraft(t, "original")
	plan := Batch(draft)
	draft.data[0] = '['
	if _, err := conversation.Commit(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	assertFixtureItems(t, conversation.Events(), []string{"original"})
}

func newFixtureDraft(testingContext *testing.T, item string) EventDraft {
	testingContext.Helper()
	draft, err := NewEventDraft(
		fixtureEventKey,
		fixturePayload{
			Items: []string{item},
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return draft
}

func commitFixtureEvent(
	requestContext context.Context,
	conversation Context,
	item string,
) (Event, error) {
	draft, err := NewEventDraft(
		fixtureEventKey,
		fixturePayload{
			Items: []string{item},
		},
	)
	if err != nil {
		return Event{}, err
	}
	result, err := conversation.Commit(requestContext, Batch(draft))
	if err != nil {
		return Event{}, err
	}
	if len(result.Events) != 1 {
		return Event{}, errors.New("session test: fixed one-event batch returned another event count")
	}
	return result.Events[0], nil
}

func assertFixtureItems(
	testingContext *testing.T,
	entries []Event,
	want []string,
) {
	testingContext.Helper()
	items := make([]string, len(entries))
	for index, entry := range entries {
		var payload fixturePayload
		if err := decodeSessionPayload(entry.Data, &payload); err != nil {
			testingContext.Fatal(err)
		}
		if len(payload.Items) != 1 {
			testingContext.Fatalf("Event %d payload = %#v", index, payload)
		}
		items[index] = payload.Items[0]
	}
	if !reflect.DeepEqual(items, want) {
		testingContext.Fatalf("Event items = %#v, want %#v", items, want)
	}
}

func waitForPendingItems(
	testingContext *testing.T,
	owner *coordinator,
	want int,
) {
	testingContext.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		owner.queue.mutex.Lock()
		pending := len(owner.queue.items) - owner.queue.head
		if pending > 0 {
			pending--
		}
		owner.queue.mutex.Unlock()
		if pending == want {
			return
		}
		if time.Now().After(deadline) {
			testingContext.Fatalf("pending queue items = %d, want %d", pending, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForQueueState(
	testingContext *testing.T,
	owner *coordinator,
	want queueState,
) {
	testingContext.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		owner.queue.mutex.Lock()
		state := owner.queue.state
		owner.queue.mutex.Unlock()
		if state == want {
			return
		}
		if time.Now().After(deadline) {
			testingContext.Fatalf("queue state = %d, want %d", state, want)
		}
		time.Sleep(time.Millisecond)
	}
}
