package sessionactor_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/projection"
	projectionactor "github.com/gorenx/goren/session/projection/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

const projectionValueName = "fixture/projection-integration"

var projectionValue = session.DefineEvent[string](projectionValueName)

type valuesProjection struct{}

func (valuesProjection) Key() string                                              { return "values" }
func (valuesProjection) StateVersion() int64                                      { return 1 }
func (valuesProjection) InitialState() (json.RawMessage, error)                   { return json.RawMessage(`[]`), nil }
func (valuesProjection) ViewState(state json.RawMessage) (json.RawMessage, error) { return state, nil }
func (valuesProjection) ApplyState(state json.RawMessage, entry session.Event) (projection.Transition, error) {
	if entry.Type != projectionValueName {
		return projection.Transition{State: state}, nil
	}
	var values []string
	if err := json.Unmarshal(state, &values); err != nil {
		return projection.Transition{}, err
	}
	var value string
	if err := json.Unmarshal(entry.Data, &value); err != nil {
		return projection.Transition{}, err
	}
	encoded, err := json.Marshal(append(values, value))
	return projection.Transition{
		State:   encoded,
		Changed: true,
	}, err
}

type projectionObserver struct {
	ready   chan struct{}
	changes chan projection.Change
}

func (*projectionObserver) PreStart(*goakt.Context) error { return nil }
func (*projectionObserver) PostStop(*goakt.Context) error { return nil }
func (sink *projectionObserver) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(projectionactor.ChangedTopic))
	case *goakt.SubscribeAck, projectionactor.Subscribed:
		sink.ready <- struct{}{}
	case projectionactor.ChangeNotice:
		sink.changes <- input.Change
	}
}

func TestSessionCommitDrivesSubscribedProjectionAndCatchesUpMissingPrefix(t *testing.T) {
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
	identity, err := goakt.GrainOf[*sessionactor.Grain](ctx, system, "conversation", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	replies := make(chan response, 8)
	driver, err := system.Spawn(ctx, "session-writer", &requester{
		target:  identity,
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	commitValue := func(value string) {
		t.Helper()
		draft, err := session.NewEventDraft(projectionValue, value)
		if err != nil {
			t.Fatal(err)
		}
		result := request[sessionactor.Committed](t, driver, replies, sessionactor.Commit{
			CreationID: "creation",
			Plan:       session.Batch(draft),
		})
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	commitValue("before")
	target, err := goakt.GrainOf[*projectionactor.Grain](ctx, system, "projections", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := system.AskGrain(ctx, target, projectionactor.Register{
		Unit: valuesProjection{},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result := payload.(projectionactor.Registered); result.Err != nil {
		t.Fatal(result.Err)
	}
	sink := &projectionObserver{
		ready:   make(chan struct{}, 2),
		changes: make(chan projection.Change, 8),
	}
	observerPID, err := system.Spawn(ctx, "projection-observer", sink)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := projectionactor.NewSubscriber(target, observerPID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := system.Spawn(ctx, "projection-subscriber", listener,
		goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll)))); err != nil {
		t.Fatal(err)
	}
	for count := 0; count < 2; count++ {
		select {
		case <-sink.ready:
		case <-time.After(time.Second):
			t.Fatal("projection subscription timed out")
		}
	}
	for index, value := range []string{"after", "tail"} {
		commitValue(value)
		select {
		case changed := <-sink.changes:
			expected := `["before","after"]`
			if index == 1 {
				expected = `["before","after","tail"]`
			}
			if changed.Seq != int64(index+1) || string(changed.Value) != expected {
				t.Fatalf("projection = %#v, expected %s", changed, expected)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Session commit did not update projection")
		}
	}
	closed := request[sessionactor.Closed](t, coordinator, completions, sessionregistry.CloseSession{
		SessionID:  "conversation",
		CreationID: "creation",
	})
	if closed.Err != nil || closed.Barrier.NextSeq != 3 {
		t.Fatalf("close = %#v", closed)
	}
	// A close reply confirms Session shutdown; wait separately for subscriber cleanup.
	deadline := time.Now().Add(time.Second)
	for {
		payload, err := system.AskGrain(ctx, target, projectionactor.Read{
			SessionID:  "conversation",
			CreationID: "creation",
		}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if payload.(projectionactor.Viewed).Err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closed Session projection was not released")
		}
		time.Sleep(time.Millisecond)
	}
	payload, err = system.AskGrain(ctx, target, sessionactor.CommitNotice{
		SessionID:  "conversation",
		CreationID: "creation",
		Event: session.Event{
			Seq:  0,
			Type: projectionValueName,
			Data: json.RawMessage(`"late"`),
		},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if payload.(projectionactor.Completion).Err == nil {
		t.Fatal("closed creation accepted a late notification")
	}

}
