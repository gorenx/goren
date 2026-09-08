package sessionactor_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type firstAppend struct{ draft session.EventDraft }

func (plan firstAppend) Build(context.Context, session.Snapshot) ([]session.EventDraft, error) {
	return []session.EventDraft{plan.draft}, nil
}
func (plan firstAppend) FindCommit(snapshot session.Snapshot) (*session.WriteResult, error) {
	if len(snapshot.Events) == 0 {
		return nil, nil
	}
	if err := plan.draft.CheckCommit(snapshot.Events[0]); err != nil {
		return nil, err
	}
	return &session.WriteResult{
		FirstSeq: 0,
		NextSeq:  1,
		Barrier: session.WriteBarrier{
			SessionID: snapshot.Barrier.SessionID,
			NextSeq:   1,
		},
		Events: snapshot.Events[:1],
	}, nil
}

type appendObserver struct {
	ready  chan struct{}
	events chan session.Event
}

func (*appendObserver) PreStart(*goakt.Context) error { return nil }
func (*appendObserver) PostStop(*goakt.Context) error { return nil }
func (sink *appendObserver) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(session.AppendedEventName))
	case *goakt.SubscribeAck:
		close(sink.ready)
	case sessionactor.CommitNotice:
		sink.events <- input.Event
	}
}

func TestIdempotentCommitDoesNotRepublishEvents(t *testing.T) {
	ctx := context.Background()
	system := startSystem(t, goakt.WithPubSub())
	coordinator, completions := registryRequester(t, system)
	initialized := request[sessionactor.Initialized](t, coordinator, completions, sessionregistry.CreateSession{
		SessionID:  "conversation",
		CreationID: "creation",
		Storage:    newStorage(t),
	})
	if initialized.Err != nil {
		t.Fatal(initialized.Err)
	}
	target, err := goakt.GrainOf[*sessionactor.Grain](ctx, system, "conversation", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	replies := make(chan response, 1)
	driver, err := system.Spawn(ctx, "writer", &requester{
		target:  target,
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	events := make(chan session.Event, 8)
	if _, err := system.Spawn(ctx, "observer", &appendObserver{
		ready:  ready,
		events: events,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("subscription timed out")
	}
	draft, err := session.NewEventDraft(session.TurnStarted, session.TurnStart{
		Turn: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := sessionactor.Commit{
		CreationID: "creation",
		Plan: firstAppend{
			draft: draft,
		},
	}
	original := request[sessionactor.Committed](t, driver, replies, input)
	repeated := request[sessionactor.Committed](t, driver, replies, input)
	if original.Err != nil || repeated.Err != nil || !reflect.DeepEqual(original.Result, repeated.Result) {
		t.Fatalf("idempotent replies: %+v, %+v", original, repeated)
	}
	// A later append from the same Session is a marker after the retry's
	// publication opportunity. Duplicate publication would precede this marker.
	marker := request[sessionactor.Committed](t, driver, replies, sessionactor.Commit{
		CreationID: "creation",
		Plan:       session.Batch(draft),
	})
	if marker.Err != nil || marker.Result.FirstSeq != 1 {
		t.Fatalf("marker: %+v", marker)
	}
	for expected := int64(0); expected < 2; expected++ {
		select {
		case entry := <-events:
			if entry.Seq != expected {
				t.Fatalf("replayed append notification: seq=%d; want %d", entry.Seq, expected)
			}
		case <-time.After(time.Second):
			t.Fatal("append notification timed out")
		}
	}
}
