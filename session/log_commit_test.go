package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

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

func TestCommitRejectsWholeBatchWhenLaterDraftCannotApply(t *testing.T) {
	t.Parallel()
	conversation, err := NewLog("atomic-batch", CreateOptions{})
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

func TestCancellationAfterBuildCommitsNothing(t *testing.T) {
	t.Parallel()
	conversation, err := NewLog("build-cancellation", CreateOptions{})
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

func TestPlanPanicDoesNotPreventNextCommit(t *testing.T) {
	t.Parallel()
	conversation, err := NewLog("plan-panic", CreateOptions{})
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

func TestBatchDetachesDraftBeforeCommit(t *testing.T) {
	t.Parallel()
	conversation, err := NewLog("batch-snapshot", CreateOptions{})
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
	conversation *Log,
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
