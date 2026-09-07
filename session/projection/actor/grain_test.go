package projectionactor_test

import (
	"context"
	"encoding/json"
	sessionactor "github.com/gorenx/goren/session/actor"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/projection"
	projectionactor "github.com/gorenx/goren/session/projection/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type latest struct{}

func (latest) Key() string                                              { return "latest" }
func (latest) StateVersion() int64                                      { return 1 }
func (latest) InitialState() (json.RawMessage, error)                   { return json.RawMessage(`null`), nil }
func (latest) ViewState(state json.RawMessage) (json.RawMessage, error) { return state, nil }
func (latest) ApplyState(_ json.RawMessage, entry session.Event) (projection.Transition, error) {
	return projection.Transition{
		State:   entry.Data,
		Changed: true,
	}, nil
}

type observer struct {
	ready   chan struct{}
	changes chan projectionactor.ChangeNotice
}

func (*observer) PreStart(*goakt.Context) error { return nil }
func (*observer) PostStop(*goakt.Context) error { return nil }
func (sink *observer) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(projectionactor.ChangedTopic))
	case *goakt.SubscribeAck:
		close(sink.ready)
	case projectionactor.ChangeNotice:
		sink.changes <- input
	}
}

func TestGrainPublishesOnlySuccessfulNewFoldsAndReturnsDetachedCheckpoints(t *testing.T) {
	ctx := context.Background()
	system, err := goakt.NewActorSystem("projection-test", goakt.WithPubSub(), goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := system.Stop(deadline); err != nil {
			t.Error(err)
		}
	})
	sink := &observer{
		ready:   make(chan struct{}),
		changes: make(chan projectionactor.ChangeNotice, 8),
	}
	if _, err := system.Spawn(ctx, "observer", sink); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.ready:
	case <-time.After(time.Second):
		t.Fatal("subscription timed out")
	}
	identity, err := goakt.GrainOf[*projectionactor.Grain](ctx, system, "projections", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	registered := ask[projectionactor.Registered](t, system, identity, projectionactor.Register{
		Unit: latest{},
	})
	if registered.Err != nil {
		t.Fatal(registered.Err)
	}
	events := []session.Event{
		{
			Seq:  0,
			Type: "fixture",
			Data: json.RawMessage(`"first"`),
		},
	}
	fact := sessionactor.CommitNotice{
		SessionID:  "conversation",
		CreationID: "creation",
		Event:      events[0],
	}
	result := ask[projectionactor.Completion](t, system, identity, fact)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	select {
	case changed := <-sink.changes:
		if changed.SessionID != "conversation" || changed.CreationID != "creation" || string(changed.Change.Value) != `"first"` {
			t.Fatalf("change = %#v", changed)
		}
		changed.Change.Value[0] = '!'
	case <-time.After(time.Second):
		t.Fatal("change not published")
	}
	duplicate := ask[projectionactor.Completion](t, system, identity, fact)
	if duplicate.Err != nil {
		t.Fatal(duplicate.Err)
	}
	captured := ask[projectionactor.Captured](t, system, identity, projectionactor.Capture{
		SessionID:  "conversation",
		CreationID: "creation",
		Events:     events,
	})
	if captured.Err != nil || string(captured.Checkpoint["latest"].Value) != `"first"` {
		t.Fatalf("capture = %#v", captured)
	}
	floor := ask[projectionactor.Floor](t, system, identity, projectionactor.RestoreFloor{
		Checkpoint: captured.Checkpoint,
	})
	if floor.Seq == nil || *floor.Seq != 0 {
		t.Fatalf("floor = %#v", floor)
	}
	restored := ask[projectionactor.Restored](t, system, identity, projectionactor.Restore{
		Checkpoint: captured.Checkpoint,
		Events:     events,
	})
	if restored.Err != nil || string(restored.Result.Snapshot.Values["latest"]) != `"first"` {
		t.Fatalf("restore = %#v", restored)
	}
	captured.Checkpoint["latest"].Value[0] = '!'
	viewed := ask[projectionactor.Viewed](t, system, identity, projectionactor.Read{
		SessionID:  "conversation",
		CreationID: "creation",
		Events:     events,
	})
	if viewed.Err != nil || string(viewed.Snapshot.Values["latest"]) != `"first"` {
		t.Fatalf("read = %#v", viewed)
	}
	replacement := sessionactor.CommitNotice{
		SessionID:  "conversation",
		CreationID: "replacement",
		Event: session.Event{
			Seq:  0,
			Type: "fixture",
			Data: json.RawMessage(`"replacement"`),
		},
	}
	if result := ask[projectionactor.Completion](t, system, identity, replacement); result.Err != nil {
		t.Fatal(result.Err)
	}
	select {
	case changed := <-sink.changes:
		if changed.CreationID != "replacement" || string(changed.Change.Value) != `"replacement"` {
			t.Fatalf("replacement notice = %#v", changed)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement change not published")
	}
	original := ask[projectionactor.Viewed](t, system, identity, projectionactor.Read{
		SessionID:  "conversation",
		CreationID: "creation",
		Events:     events,
	})
	if original.Err != nil || string(original.Snapshot.Values["latest"]) != `"first"` {
		t.Fatalf("creation states mixed: %#v", original)
	}
	forgotten := ask[projectionactor.Completion](t, system, identity, projectionactor.Forget{
		SessionID:  "conversation",
		CreationID: "creation",
	})
	if forgotten.Err != nil {
		t.Fatal(forgotten.Err)
	}
	if result := ask[projectionactor.Completion](t, system, identity, replacement); result.Err != nil {
		t.Fatal(result.Err)
	}
	late := ask[projectionactor.Completion](t, system, identity, fact)
	if late.Err == nil {
		t.Fatal("released creation was rebuilt by a late commit notice")
	}
	oldClose := ask[projectionactor.Completion](t, system, identity, sessionactor.ClosedNotice{
		SessionID:  "conversation",
		CreationID: "creation",
	})
	if oldClose.Err != nil {
		t.Fatal(oldClose.Err)
	}
	if result := ask[projectionactor.Completion](t, system, identity, replacement); result.Err != nil {
		t.Fatal(result.Err)
	}
	removed := ask[projectionactor.Completion](t, system, identity, projectionactor.Remove{
		ID: registered.ID,
	})
	if removed.Err != nil {
		t.Fatal(removed.Err)
	}
	empty := ask[projectionactor.Viewed](t, system, identity, projectionactor.Read{
		SessionID:  "conversation",
		CreationID: "replacement",
		Events:     events,
	})
	if empty.Err != nil || len(empty.Snapshot.Values) != 0 {
		t.Fatalf("removed = %#v", empty)
	}
	select {
	case extra := <-sink.changes:
		t.Fatalf("unexpected notification: %#v", extra)
	case <-time.After(30 * time.Millisecond):
	}
}

func ask[T any](t *testing.T, system goakt.ActorSystem, identity *goakt.GrainIdentity, input any) T {
	t.Helper()
	payload, err := system.AskGrain(context.Background(), identity, input, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := payload.(T)
	if !ok {
		t.Fatalf("unexpected response %T", payload)
	}
	return result
}
