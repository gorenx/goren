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
	toolregistry "github.com/gorenx/goren/tools/registry"
	toolactor "github.com/gorenx/goren/tools/registry/actor"
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

func publishTools(t *testing.T, ctx context.Context, system goakt.ActorSystem, identifier session.SessionID, names ...string) {
	t.Helper()
	registry, err := goakt.GrainOf[*toolactor.RegistryGrain](ctx, system, toolactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	update := toolregistry.UpdateID(uuid.NewString())
	commands := []any{toolactor.BeginUpdate{
		Update: toolregistry.Update{
			ID:      update,
			AgentID: identifier,
			Names:   names,
		},
	}}
	for _, name := range names {
		commands = append(commands, toolactor.RegisterDefinition{
			ID: update,
			Candidate: toolregistry.Candidate{
				Schema: llm.ToolSchema{
					Name:       name,
					Parameters: []byte(`{"type":"object"}`),
				},
				Visible: true,
			},
		})
	}
	for _, command := range commands {
		payload, err := system.AskGrain(ctx, registry, command, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		result := payload.(toolactor.Completion)
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	payload, err := system.AskGrain(ctx, registry, toolactor.PublishDefinitions{
		ID: update,
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if payload.(toolactor.Catalog).Err != nil {
		t.Fatal(payload)
	}
	payload, err = system.AskGrain(ctx, registry, toolactor.SettleUpdate{
		ID: update,
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if payload.(toolactor.Completion).Err != nil {
		t.Fatal(payload)
	}
}

func TestToolsSubscriberAppliesPublishedViews(t *testing.T) {
	_, system, _ := startRegistry(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	target, err := goakt.GrainOf[*promptactor.RuntimeGrain](ctx, system, promptactor.RuntimeName("agent"), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := initializeRuntime(t, ctx, system, target, "agent", systemprompt.DeriveLayer(systemprompt.Snapshot{}).Capture(), nil)
	if err != nil || payload.(promptactor.RuntimeInitialized).Err != nil {
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
				ID: fmt.Sprint(version),
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
	publishTools(t, ctx, system, "agent", "read")
	if applied := receiveEvent[promptactor.ToolsApplied](t, ctx, events); applied.Err != nil {
		t.Fatalf("application: %#v", applied)
	}
	check(2, "read")
	// A delayed or malformed notification is a refresh hint, never a catalog replacement.
	for _, name := range []string{"old", "duplicate"} {
		err := goakt.Tell(ctx, subscriber, tools.Definitions{
			AgentID: "agent",
			Schemas: []llm.ToolSchema{{
				Name: name,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		applied := receiveEvent[promptactor.ToolsApplied](t, ctx, events)
		if applied.Err != nil {
			t.Fatal(applied.Err)
		}
		check(2, "read")
	}
	publishTools(t, ctx, system, "other-agent", "foreign")
	publishTools(t, ctx, system, "agent")
	if applied := receiveEvent[promptactor.ToolsApplied](t, ctx, events); applied.Err != nil || applied.AgentID != "agent" {
		t.Fatalf("Agent filtering: %#v", applied)
	}
	check(4, "")
	err = goakt.Tell(ctx, subscriber, tools.Definitions{
		AgentID: "agent",
		Schemas: []llm.ToolSchema{{Name: "read"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	applied := receiveEvent[promptactor.ToolsApplied](t, ctx, events)
	if applied.Err != nil {
		t.Fatal(applied.Err)
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
	Views   chan<- systemprompt.ToolDefinitions
}
type releaseUpdate struct{}

// DelayedToolsGrain holds a reply so topic removal can be observed independently.
type DelayedToolsGrain struct {
	entered chan<- struct{}
	views   chan<- systemprompt.ToolDefinitions
	reply   *goakt.GrainReply
}

func (*DelayedToolsGrain) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*DelayedToolsGrain) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *DelayedToolsGrain) OnReceive(gc *goakt.GrainContext) {
	switch incoming := gc.Message().(type) {
	case installDelay:
		owner.entered = incoming.Entered
		owner.views = incoming.Views
		gc.Response(promptactor.Completion{})
	case promptactor.ApplyTools:
		owner.reply = gc.DeferResponse()
		owner.entered <- struct{}{}
		if owner.views != nil {
			owner.views <- incoming.Definitions
		}
	case releaseUpdate:
		owner.reply.Response(promptactor.Completion{
			Err: systemprompt.ErrAgent,
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
	publishTools(t, ctx, system, "agent")
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
	if !errors.Is(applied.Err, systemprompt.ErrAgent) {
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
		toolactor.BeginUpdate{
			Update: toolregistry.Update{
				ID:      "initial",
				AgentID: "agent",
				Names:   []string{"read"},
			},
		},
		toolactor.RegisterDefinition{
			ID: "initial",
			Candidate: toolregistry.Candidate{
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
	payload, err := system.AskGrain(ctx, registry, toolactor.PublishDefinitions{
		ID: "initial",
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if committed := payload.(toolactor.Catalog); committed.Err != nil {
		t.Fatalf("publication: %#v", committed)
	}
	target, err := goakt.GrainOf[*promptactor.RuntimeGrain](ctx, system, promptactor.RuntimeName("agent"), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	payload, err = initializeRuntime(t, ctx, system, target, "agent", systemprompt.DeriveLayer(systemprompt.Snapshot{}).Capture(), registry)
	if err != nil || payload.(promptactor.RuntimeInitialized).Err != nil {
		t.Fatalf("initialize: %#v, %v", payload, err)
	}
	subscriber, events := startSubscriber(t, ctx, system, target)
	if baseline := receiveEvent[promptactor.ToolsApplied](t, ctx, events); baseline.Err != nil {
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
			ID: "baseline",
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
	if refreshed := receiveEvent[promptactor.ToolsApplied](t, ctx, events); refreshed.Err != nil {
		t.Fatalf("refresh: %#v", refreshed)
	}
}

func TestToolsRefreshAppliesLatestMembershipAfterPendingApplication(t *testing.T) {
	_, system, _ := startRegistry(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	target, err := goakt.GrainOf[*DelayedToolsGrain](ctx, system, "delayed-refresh", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 4)
	views := make(chan systemprompt.ToolDefinitions, 4)
	_, err = system.AskGrain(ctx, target, installDelay{
		Entered: entered,
		Views:   views,
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	subscriber, events := startSubscriber(t, ctx, system, target)
	baseline := receiveEvent[promptactor.ToolsApplied](t, ctx, events)
	if !errors.Is(baseline.Err, systemprompt.ErrToolsNotReady) {
		t.Fatal(baseline.Err)
	}
	publishTools(t, ctx, system, "agent", "first")
	select {
	case initial := <-views:
		if len(initial.Schemas) != 1 || initial.Schemas[0].Name != "first" {
			t.Fatal(initial)
		}
	case <-ctx.Done():
		t.Fatal("first application did not arrive")
	}
	publishTools(t, ctx, system, "agent", "replacement")
	publishTools(t, ctx, system, "agent")
	_, err = system.AskGrain(ctx, target, releaseUpdate{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	receiveEvent[promptactor.ToolsApplied](t, ctx, events)
	select {
	case latest := <-views:
		if len(latest.Schemas) != 0 || len(latest.KnownNames) != 0 {
			t.Fatal("refresh applied obsolete membership", latest)
		}
	case <-ctx.Done():
		t.Fatal("pending refresh did not continue")
	}
	err = goakt.Tell(ctx, subscriber, promptactor.UnsubscribeTools{})
	if err != nil {
		t.Fatal(err)
	}
	receiveEvent[promptactor.ToolsUnsubscribed](t, ctx, events)
	_, err = system.AskGrain(ctx, target, releaseUpdate{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	receiveEvent[promptactor.ToolsApplied](t, ctx, events)
}
