package toolactor_test

import (
	"context"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	toolactor "github.com/gorenx/goren/tools/registry/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type observer struct {
	ready  chan struct{}
	events chan tools.Definitions
}

func (*observer) PreStart(*goakt.Context) error { return nil }
func (*observer) PostStop(*goakt.Context) error { return nil }
func (probe *observer) Receive(rc *goakt.ReceiveContext) {
	switch incoming := rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(tools.DefinitionsTopic))
	case *goakt.SubscribeAck:
		probe.ready <- struct{}{}
	case tools.Definitions:
		probe.events <- incoming
	}
}

func startRegistry(t *testing.T, pubsub bool) (goakt.ActorSystem, *goakt.GrainIdentity) {
	t.Helper()
	options := []goakt.Option{goakt.WithLogger(log.DiscardLogger)}
	if pubsub {
		options = append(options, goakt.WithPubSub())
	}
	system, err := goakt.NewActorSystem("tools-test", options...)
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	identity, err := goakt.GrainOf[*toolactor.RegistryGrain](context.Background(), system, toolactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	return system, identity
}

func ask[T any](t *testing.T, system goakt.ActorSystem, target *goakt.GrainIdentity, message any) T {
	t.Helper()
	incoming, err := system.AskGrain(context.Background(), target, message, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reply, ok := incoming.(T)
	if !ok {
		t.Fatalf("unexpected reply %T", incoming)
	}
	return reply
}

func begin(t *testing.T, system goakt.ActorSystem, target *goakt.GrainIdentity) {
	t.Helper()
	result := ask[toolactor.Completion](t, system, target, toolactor.BeginUpdate{
		Update: toolregistry.Update{
			ID:      "update",
			AgentID: "agent",
			Names:   []string{"echo"},
		},
	})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
}

func register(t *testing.T, system goakt.ActorSystem, target *goakt.GrainIdentity) {
	t.Helper()
	result := ask[toolactor.Completion](t, system, target, toolactor.RegisterDefinition{
		ID: "update",
		Candidate: toolregistry.Candidate{
			Schema: llm.ToolSchema{
				Name:       "echo",
				Parameters: []byte(`{"type":"object"}`),
			},
			Visible: true,
		},
	})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
}

func TestRegistryGrainPublishesOnlyCompleteCatalog(t *testing.T) {
	system, target := startRegistry(t, true)
	probe := &observer{
		ready:  make(chan struct{}, 1),
		events: make(chan tools.Definitions, 4),
	}
	if _, err := system.Spawn(context.Background(), "observer", probe); err != nil {
		t.Fatal(err)
	}
	select {
	case <-probe.ready:
	case <-time.After(time.Second):
		t.Fatal("subscription timeout")
	}
	begin(t, system, target)
	failed := ask[toolactor.Catalog](t, system, target, toolactor.PublishDefinitions{
		ID: "update",
	})
	if failed.Err == nil || failed.Found {
		t.Fatalf("partial publication = %#v", failed)
	}
	register(t, system, target)
	committed := ask[toolactor.Catalog](t, system, target, toolactor.PublishDefinitions{
		ID: "update",
	})
	if committed.Err != nil || !committed.Found || committed.Definitions.Schemas[0].Version != 1 {
		t.Fatalf("publication = %#v", committed)
	}
	committed.Definitions.Schemas[0].Parameters[0] = '!'
	select {
	case event := <-probe.events:
		if event.Schemas[0].Version != 1 || event.AgentID != "agent" || string(event.Schemas[0].Parameters) != `{"type":"object"}` {
			t.Fatalf("event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("publication timeout")
	}
	baseline := ask[toolactor.Catalog](t, system, target, toolactor.ReadDefinitions{
		AgentID: "agent",
	})
	if !baseline.Found || baseline.Definitions.Schemas[0].Version != 1 {
		t.Fatalf("baseline = %#v", baseline)
	}
	outcome := ask[toolactor.Outcome](t, system, target, toolactor.ReadPublication{
		ID: "update",
	})
	if !outcome.Found || !outcome.Publication.Published || outcome.Publication.Catalog.Schemas[0].Version != 1 {
		t.Fatalf("outcome = %#v", outcome)
	}
	repeated := ask[toolactor.Catalog](t, system, target, toolactor.PublishDefinitions{
		ID: "update",
	})
	if repeated.Err != nil || repeated.Definitions.Schemas[0].Version != 1 {
		t.Fatalf("retry = %#v", repeated)
	}
}

func TestRegistryGrainRetainsCommitWhenNotificationUnavailable(t *testing.T) {
	system, target := startRegistry(t, false)
	begin(t, system, target)
	register(t, system, target)
	committed := ask[toolactor.Catalog](t, system, target, toolactor.PublishDefinitions{
		ID: "update",
	})
	if committed.Err == nil || !committed.Found || committed.Definitions.Schemas[0].Version != 1 {
		t.Fatalf("notification failure = %#v", committed)
	}
	outcome := ask[toolactor.Outcome](t, system, target, toolactor.ReadPublication{
		ID: "update",
	})
	if !outcome.Found || !outcome.Publication.Published {
		t.Fatalf("lost commit = %#v", outcome)
	}
}
