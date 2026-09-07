package promptactor_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	"github.com/gorenx/goren/tools"
	toolactor "github.com/gorenx/goren/tools/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type subscriptionProbe struct {
	events chan any
}

func (*subscriptionProbe) PreStart(*goakt.Context) error { return nil }
func (*subscriptionProbe) PostStop(*goakt.Context) error { return nil }
func (observer *subscriptionProbe) Receive(rc *goakt.ReceiveContext) {
	switch incoming := rc.Message().(type) {
	case promptactor.ToolsSubscribed, promptactor.ToolsApplied, promptactor.ToolsUnsubscribed:
		observer.events <- incoming
	}
}

func receiveEvent[T any](t *testing.T, ctx context.Context, events <-chan any) T {
	t.Helper()
	select {
	case incoming := <-events:
		value, ok := incoming.(T)
		if !ok {
			t.Fatalf("unexpected subscription event: %#v", incoming)
		}
		return value
	case <-ctx.Done():
		t.Fatal("subscription event timed out")
		var empty T
		return empty
	}
}

func startSubscriber(t *testing.T, ctx context.Context, system goakt.ActorSystem, target *goakt.GrainIdentity) (*goakt.PID, <-chan any) {
	t.Helper()
	events := make(chan any, 16)
	coordinator, err := system.Spawn(ctx, "subscription-probe", &subscriptionProbe{
		events: events,
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := goakt.GrainOf[*toolactor.RegistryGrain](ctx, system, toolactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	forwarder, err := promptactor.NewToolsSubscriber("agent", target, registry, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := system.Spawn(ctx, "tools-subscriber", forwarder,
		goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	if ready := receiveEvent[promptactor.ToolsSubscribed](t, ctx, events); ready.AgentID != "agent" {
		t.Fatalf("subscription identity = %q", ready.AgentID)
	}
	return pid, events
}

func publishTools(t *testing.T, ctx context.Context, system goakt.ActorSystem, identifier session.SessionID, version uint64, names ...string) {
	t.Helper()
	definitions := tools.Definitions{}
	for _, name := range names {
		definitions.Schemas = append(definitions.Schemas, llm.ToolSchema{
			Name:       name,
			Parameters: []byte(`{"type":"object"}`),
		})
	}
	if err := goakt.Tell(ctx, system.TopicActor(), goakt.NewPublish(uuid.NewString(), tools.DefinitionsTopic, tools.DefinitionsChanged{
		AgentID:     identifier,
		Version:     version,
		Definitions: definitions,
	})); err != nil {
		t.Fatal(err)
	}
}

func TestToolsSubscriberAppliesPublishedViews(t *testing.T) {
	_, system, _ := startRegistry(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	target, err := goakt.GrainOf[*promptactor.RuntimeGrain](ctx, system, "agent", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := system.AskGrain(ctx, target, promptactor.InitializeRuntime{
		AgentID:     "agent",
		Definitions: systemprompt.DeriveLayer(systemprompt.Snapshot{}).Capture(),
	}, time.Second)
	if err != nil || payload.(promptactor.Completion).Err != nil {
		t.Fatalf("initialize: %v, %v", payload, err)
	}
	subscriber, events := startSubscriber(t, ctx, system, target)
	if baseline := receiveEvent[promptactor.ToolsApplied](t, ctx, events); !errors.Is(baseline.Err, systemprompt.ErrToolsNotReady) {
		t.Fatalf("missing baseline = %#v", baseline)
	}
	completed := make(chan promptactor.Assembled, 1)
	consumer, err := system.Spawn(ctx, "subscription-assembly", &assemblyRequester{
		replies: completed,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	check := func(version uint64, expected string) {
		t.Helper()
		if err := goakt.Tell(ctx, consumer, assemblyRequest{
			Target: target,
			Input: promptactor.Assemble{
				ID:           fmt.Sprint(version),
				ToolsVersion: version,
			},
		}); err != nil {
			t.Fatal(err)
		}
		select {
		case reply := <-completed:
			if reply.Err != nil {
				t.Fatal(reply.Err)
			}
			if expected == "" {
				if len(reply.Prompt.Tools) != 0 {
					t.Fatal("empty event retained old tools")
				}
			} else if len(reply.Prompt.Tools) != 1 || reply.Prompt.Tools[0].Name != expected {
				t.Fatalf("unexpected tools: %#v", reply.Prompt.Tools)
			}
		case <-ctx.Done():
			t.Fatal("assembly timed out")
		}
	}
	publishTools(t, ctx, system, "agent", 2, "read")
	if applied := receiveEvent[promptactor.ToolsApplied](t, ctx, events); applied.Err != nil || applied.Version != 2 {
		t.Fatalf("application: %#v", applied)
	}
	check(2, "read")
	publishTools(t, ctx, system, "agent", 1, "old")
	if applied := receiveEvent[promptactor.ToolsApplied](t, ctx, events); applied.Err != nil || applied.Version != 1 {
		t.Fatalf("old event: %#v", applied)
	}
	check(2, "read")
	publishTools(t, ctx, system, "agent", 3, "duplicate", "duplicate")
	if applied := receiveEvent[promptactor.ToolsApplied](t, ctx, events); applied.Err == nil {
		t.Fatal("invalid catalog was accepted")
	}
	check(2, "read")
	publishTools(t, ctx, system, "other-agent", 99, "foreign")
	publishTools(t, ctx, system, "agent", 4)
	if applied := receiveEvent[promptactor.ToolsApplied](t, ctx, events); applied.Err != nil || applied.AgentID != "agent" || applied.Version != 4 {
		t.Fatalf("Agent filtering: %#v", applied)
	}
	check(4, "")
	if err := goakt.Tell(ctx, subscriber, promptactor.UnsubscribeTools{}); err != nil {
		t.Fatal(err)
	}
	if stopped := receiveEvent[promptactor.ToolsUnsubscribed](t, ctx, events); stopped.AgentID != "agent" {
		t.Fatal("wrong unsubscribe identity")
	}
}

type installDelay struct {
	Entered chan<- struct{}
}
type releaseUpdate struct{}

// DelayedToolsGrain holds a reply so topic removal can be observed independently.
type DelayedToolsGrain struct {
	entered chan<- struct{}
	reply   *goakt.GrainReply
}

func (*DelayedToolsGrain) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*DelayedToolsGrain) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *DelayedToolsGrain) OnReceive(gc *goakt.GrainContext) {
	switch incoming := gc.Message().(type) {
	case installDelay:
		owner.entered = incoming.Entered
		gc.Response(promptactor.Completion{})
	case promptactor.ApplyTools:
		owner.reply = gc.DeferResponse()
		owner.entered <- struct{}{}
	case releaseUpdate:
		owner.reply.Response(promptactor.Completion{
			Err: systemprompt.ErrToolsVersion,
		})
		gc.Response(promptactor.Completion{})
	}
}

func TestToolsUnsubscribeDoesNotWaitForRuntimeReply(t *testing.T) {
	_, system, _ := startRegistry(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	target, err := goakt.GrainOf[*DelayedToolsGrain](ctx, system, "delayed", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	if _, err := system.AskGrain(ctx, target, installDelay{
		Entered: entered,
	}, time.Second); err != nil {
		t.Fatal(err)
	}
	subscriber, events := startSubscriber(t, ctx, system, target)
	if baseline := receiveEvent[promptactor.ToolsApplied](t, ctx, events); !errors.Is(baseline.Err, systemprompt.ErrToolsNotReady) {
		t.Fatalf("missing baseline = %#v", baseline)
	}
	publishTools(t, ctx, system, "agent", 1)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("update did not reach target")
	}
	if err := goakt.Tell(ctx, subscriber, promptactor.UnsubscribeTools{}); err != nil {
		t.Fatal(err)
	}
	receiveEvent[promptactor.ToolsUnsubscribed](t, ctx, events)
	if _, err := system.AskGrain(ctx, target, releaseUpdate{}, time.Second); err != nil {
		t.Fatal(err)
	}
	applied := receiveEvent[promptactor.ToolsApplied](t, ctx, events)
	if applied.Version != 1 || !errors.Is(applied.Err, systemprompt.ErrToolsVersion) {
		t.Fatalf("late outcome lost: %#v", applied)
	}
}

func TestToolsSubscriberReadsCatalogPublishedBeforeSubscription(t *testing.T) {
	_, system, _ := startRegistry(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := goakt.GrainOf[*toolactor.RegistryGrain](ctx, system, toolactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []any{
		toolactor.Begin{
			Update: tools.Update{
				ID:      "initial",
				AgentID: "agent",
				Names:   []string{"read"},
			},
		},
		toolactor.Register{
			ID: "initial",
			Candidate: tools.Candidate{
				Schema: llm.ToolSchema{
					Name:       "read",
					Parameters: []byte(`{"type":"object"}`),
				},
				Visible: true,
			},
		},
	} {
		reply, err := system.AskGrain(ctx, registry, command, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if completion, ok := reply.(toolactor.Completion); !ok || completion.Err != nil {
			t.Fatalf("registration: %#v", reply)
		}
	}
	payload, err := system.AskGrain(ctx, registry, toolactor.Publish{
		ID: "initial",
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if committed := payload.(toolactor.Catalog); committed.Err != nil || committed.Definitions.Version != 1 {
		t.Fatalf("publication: %#v", committed)
	}
	target, err := goakt.GrainOf[*promptactor.RuntimeGrain](ctx, system, "agent", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	payload, err = system.AskGrain(ctx, target, promptactor.InitializeRuntime{
		AgentID:     "agent",
		Definitions: systemprompt.DeriveLayer(systemprompt.Snapshot{}).Capture(),
	}, time.Second)
	if err != nil || payload.(promptactor.Completion).Err != nil {
		t.Fatalf("initialize: %#v, %v", payload, err)
	}
	subscriber, events := startSubscriber(t, ctx, system, target)
	if baseline := receiveEvent[promptactor.ToolsApplied](t, ctx, events); baseline.Err != nil || baseline.Version != 1 {
		t.Fatalf("baseline: %#v", baseline)
	}
	// Assembly observes the baseline through the real Prompt Runtime, not a probe.
	replies := make(chan promptactor.Assembled, 1)
	requester, err := system.Spawn(ctx, "baseline-assembly", &assemblyRequester{
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	if err := goakt.Tell(ctx, requester, assemblyRequest{
		Target: target,
		Input: promptactor.Assemble{
			ID:           "baseline",
			ToolsVersion: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-replies:
		if reply.Err != nil || len(reply.Prompt.Tools) != 1 || reply.Prompt.Tools[0].Name != "read" {
			t.Fatalf("assembly: %#v", reply)
		}
	case <-ctx.Done():
		t.Fatal("baseline assembly timeout")
	}
	if err := goakt.Tell(ctx, subscriber, promptactor.RefreshTools{}); err != nil {
		t.Fatal(err)
	}
	if refreshed := receiveEvent[promptactor.ToolsApplied](t, ctx, events); refreshed.Err != nil || refreshed.Version != 1 {
		t.Fatalf("refresh: %#v", refreshed)
	}
}
