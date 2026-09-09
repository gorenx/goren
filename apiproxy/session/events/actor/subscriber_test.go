package eventadapter_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	sessionevents "github.com/gorenx/goren/apiproxy/session/events"
	eventadapter "github.com/gorenx/goren/apiproxy/session/events/actor"
	"github.com/gorenx/goren/apiproxy/stream"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type blockedConsumer struct {
	feed     *sessionevents.Feed
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (consumer *blockedConsumer) Committed(ctx context.Context, identifier session.SessionID, creationID string, event session.Event) error {
	if event.Seq == 0 {
		close(consumer.started)
		select {
		case <-consumer.release:
		case <-ctx.Done():
			close(consumer.finished)
			return ctx.Err()
		}
	}
	return consumer.feed.Committed(ctx, identifier, creationID, event)
}

type unusedSnapshots struct{ sessionevents.Snapshots }

func TestSubscriberPublishesThroughFeedInCommitOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	system := startSubscriberSystem(t)
	broker, err := stream.NewBroker(stream.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	feed, err := sessionevents.NewFeed(unusedSnapshots{}, broker)
	if err != nil {
		t.Fatal(err)
	}
	consumer := &blockedConsumer{
		feed:     feed,
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	endpoint, err := eventadapter.SpawnSubscriber(ctx, system, "commits", consumer)
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Shutdown(context.Background())
	output, err := broker.OpenMux()
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Release(output)
	publishCommit(t, ctx, system, 0)
	select {
	case <-consumer.started:
	case <-ctx.Done():
		t.Fatal("first consumer did not start")
	}
	publishCommit(t, ctx, system, 1)
	// The first callback is held while a later event is available to PubSub.
	// No frame may bypass it, even though the Actor mailbox is free to run.
	probe, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	_, found, _ := output.Next(probe)
	stop()
	if found {
		t.Fatal("later commit bypassed blocked consumption")
	}
	close(consumer.release)
	for _, seq := range []int64{0, 1} {
		batch, found, err := output.Next(ctx)
		if err != nil || !found || len(batch) != 1 {
			t.Fatalf("missing frame: %#v, %v", batch, err)
		}
		var frame struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionId"`
			Event     struct {
				Seq int64 `json:"seq"`
			} `json:"event"`
		}
		if err := json.Unmarshal(batch[0].Payload, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type != "session/event" || frame.SessionID != "conversation" || frame.Event.Seq != seq {
			t.Fatalf("event order: %#v, want seq %d", frame, seq)
		}
	}
}

func TestSubscriberShutdownCancelsAndWaitsForConsumption(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	system := startSubscriberSystem(t)
	consumer := &blockedConsumer{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	endpoint, err := eventadapter.SpawnSubscriber(ctx, system, "commits", consumer)
	if err != nil {
		t.Fatal(err)
	}
	publishCommit(t, ctx, system, 0)
	select {
	case <-consumer.started:
	case <-ctx.Done():
		t.Fatal("consumer did not start")
	}
	if err := endpoint.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-consumer.finished:
	default:
		t.Fatal("shutdown returned before consumption exited")
	}
}

func startSubscriberSystem(t *testing.T) goakt.ActorSystem {
	t.Helper()
	system, err := goakt.NewActorSystem("session-commits", goakt.WithPubSub(), goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return system
}

func publishCommit(t *testing.T, ctx context.Context, system goakt.ActorSystem, seq int64) {
	t.Helper()
	fact := sessionactor.CommitNotice{
		SessionID:  "conversation",
		CreationID: "current",
		Event: session.Event{
			Type: "turn/start",
			Seq:  seq,
			Time: seq + 1,
			Data: json.RawMessage(`{"turn":1}`),
		},
	}
	if err := goakt.Tell(ctx, system.TopicActor(), goakt.NewPublish(uuid.NewString(), session.AppendedEventName, fact)); err != nil {
		t.Fatal(err)
	}
}
