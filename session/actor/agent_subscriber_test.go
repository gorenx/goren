package sessionactor_test

import (
	"context"
	"errors"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/session/subscriber"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type resultsSubscribed struct{}

type initializationObserver struct {
	events chan any
}

func (*initializationObserver) PreStart(*goakt.Context) error { return nil }
func (*initializationObserver) PostStop(*goakt.Context) error { return nil }
func (observer *initializationObserver) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(subscriber.InitializedTopic))
	case *goakt.SubscribeAck:
		if input.Topic() == subscriber.InitializedTopic {
			observer.events <- resultsSubscribed{}
		}
	case subscriber.AgentsSubscribed, subscriber.InitializedNotice:
		observer.events <- input
	}
}

func startCreationSubscriber(t *testing.T, system goakt.ActorSystem) <-chan any {
	t.Helper()
	events := make(chan any, 8)
	observer, err := system.Spawn(context.Background(), "initialization-observer", &initializationObserver{
		events: events,
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := goakt.GrainOf[*sessionregistry.RegistryGrain](context.Background(), system, sessionregistry.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	listener, err := subscriber.NewAgentSubscriber(registry, observer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := system.Spawn(context.Background(), "agent-subscriber", listener,
		goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll)))); err != nil {
		t.Fatal(err)
	}
	triggersReady := false
	resultsReady := false
	for !triggersReady || !resultsReady {
		select {
		case event := <-events:
			switch event.(type) {
			case resultsSubscribed:
				resultsReady = true
			case subscriber.AgentsSubscribed:
				triggersReady = true
			default:
				t.Fatalf("unexpected startup event %T", event)
			}
		case <-time.After(time.Second):
			t.Fatal("subscriptions did not become ready")
		}
	}
	return events
}

func publishCreation(t *testing.T, system goakt.ActorSystem, creationID string) {
	t.Helper()
	if err := goakt.Tell(context.Background(), system.TopicActor(), goakt.NewPublish(uuid.NewString(), agent.CreatingTopic, agent.CreatingNotice{
		AgentID:    "conversation",
		CreationID: creationID,
	})); err != nil {
		t.Fatal(err)
	}
}

func initializationEvent(t *testing.T, events <-chan any) subscriber.InitializedNotice {
	t.Helper()
	select {
	case event := <-events:
		result, ok := event.(subscriber.InitializedNotice)
		if !ok {
			t.Fatalf("unexpected event %T", event)
		}
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("Session initialization event timed out")
		return subscriber.InitializedNotice{}
	}
}

func TestCreatingEventUsesRegisteredInputWithoutEarlyStorageEffects(t *testing.T) {
	backend := &observedStorage{
		RecoveryStorage: newStorage(t),
	}
	system := startSystem(t, goakt.WithPubSub())
	driver, replies := registryRequester(t, system)
	events := startCreationSubscriber(t, system)
	cwd := t.TempDir()
	expectedCWD := cwd
	registered := request[sessionregistry.Completion](t, driver, replies, sessionregistry.RegisterSession{
		SessionID:  "conversation",
		CreationID: "creation",
		Options: session.CreateOptions{
			Seed: []session.Event{},
			Metadata: session.Metadata{
				CWD: &cwd,
			},
		},
		Storage: backend,
	})
	if registered.Err != nil {
		t.Fatal(registered.Err)
	}
	cwd = "changed after registration"
	if backend.reads.Load() != 0 || backend.writes.Load() != 0 {
		t.Fatal("registration accessed storage")
	}
	for _, identity := range system.Grains(context.Background(), time.Second) {
		if identity.Name() == "conversation" {
			t.Fatal("registration activated Session")
		}
	}
	publishCreation(t, system, "stale")
	rejected := initializationEvent(t, events)
	if rejected.Err == nil || rejected.CreationID != "stale" {
		t.Fatalf("stale trigger = %#v", rejected)
	}
	if backend.reads.Load() != 0 || backend.writes.Load() != 0 {
		t.Fatal("stale event accessed storage")
	}
	for index := 0; index < 2; index++ {
		publishCreation(t, system, "creation")
		result := initializationEvent(t, events)
		if result.Err != nil || result.SessionID != "conversation" || result.CreationID != "creation" || result.Header.CWD == nil || *result.Header.CWD != expectedCWD || result.Barrier.NextSeq != 1 {
			t.Fatalf("initialization = %#v", result)
		}
	}
	closed := request[sessionactor.Closed](t, driver, replies, sessionregistry.CloseSession{
		SessionID:  "conversation",
		CreationID: "creation",
	})
	if closed.Err != nil {
		t.Fatal(closed.Err)
	}
	if backend.writes.Load() != 1 {
		t.Fatalf("duplicate initialization wrote %d batches", backend.writes.Load())
	}
	publishCreation(t, system, "creation")
	late := initializationEvent(t, events)
	if late.Err == nil {
		t.Fatal("closed creation was reopened by stale event")
	}
}

func TestCreatingEventReportsMissingRegistrationAndRecoveryFailure(t *testing.T) {
	system := startSystem(t, goakt.WithPubSub())
	driver, replies := registryRequester(t, system)
	events := startCreationSubscriber(t, system)
	publishCreation(t, system, "creation")
	missing := initializationEvent(t, events)
	if missing.Err == nil {
		t.Fatal("missing registration reported ready")
	}
	registered := request[sessionregistry.Completion](t, driver, replies, sessionregistry.RegisterSession{
		SessionID:  "conversation",
		CreationID: "creation",
		Restore:    true,
		Storage:    newStorage(t),
	})
	if registered.Err != nil {
		t.Fatal(registered.Err)
	}
	publishCreation(t, system, "creation")
	failed := initializationEvent(t, events)
	var notFound *persistence.NotFoundError
	if !errors.As(failed.Err, &notFound) {
		t.Fatalf("recovery = %#v", failed)
	}
	closed := request[sessionactor.Closed](t, driver, replies, sessionregistry.CloseSession{
		SessionID:  "conversation",
		CreationID: "creation",
	})
	if closed.Err != nil {
		t.Fatal(closed.Err)
	}
}
