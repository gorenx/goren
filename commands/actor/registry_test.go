package commandactor

import (
	"context"
	"github.com/gorenx/goren/commands"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"testing"
	"time"
)

func TestRegistryPublishesMatchingDefinitionAndHandler(t *testing.T) {
	ctx := context.Background()
	system, err := goakt.NewActorSystem("command-registry", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	target, err := goakt.GrainOf[*Registry](ctx, system, RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	ask := func(message any) any {
		t.Helper()
		reply, err := system.AskGrain(ctx, target, message, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return reply
	}
	handler := target
	definition := commands.Definition{
		Name:        "compact",
		Description: "Compact history",
	}
	if reply := ask(Register{
		ID:         "first",
		Definition: definition,
	}).(Registered); reply.Err == nil {
		t.Fatal("missing handler accepted")
	}
	if reply := ask(Resolve{
		Name: "compact",
	}).(Resolved); reply.Found {
		t.Fatal("invalid registration published metadata")
	}
	first := ask(Register{
		ID:         "first",
		Definition: definition,
		Handler:    handler,
	}).(Registered)
	if first.Err != nil {
		t.Fatal(first.Err)
	}
	resolved := ask(Resolve{
		Name: "compact",
	}).(Resolved)
	if !resolved.Found || resolved.Handler.String() != handler.String() || resolved.Registration.ID != first.Registration.ID {
		t.Fatalf("resolution: %#v", resolved)
	}
	if reply := ask(Unregister{
		ID: first.ID,
	}).(Unregistered); !reply.Removed {
		t.Fatal("withdrawal failed")
	}
	second := ask(Register{
		ID:         "second",
		Definition: definition,
		Handler:    handler,
	}).(Registered)
	if second.Err != nil {
		t.Fatal(second.Err)
	}
	if reply := ask(Unregister{
		ID: first.ID,
	}).(Unregistered); reply.Removed {
		t.Fatal("stale withdrawal removed replacement")
	}
	resolved = ask(Resolve{
		Name: "compact",
	}).(Resolved)
	if !resolved.Found || resolved.Registration.ID != second.Registration.ID || resolved.Handler == nil {
		t.Fatal("replacement lost its handler")
	}
	if reply := ask(List{}).(Descriptors); len(reply.Values) != 1 {
		t.Fatalf("descriptors: %#v", reply)
	}
}

func TestRegistryInheritsDefaultsWithoutMixingSessionAddresses(t *testing.T) {
	ctx := context.Background()
	system, err := goakt.NewActorSystem("commands-inheritance", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	target, err := goakt.GrainOf[*Registry](ctx, system, RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := goakt.GrainOf[*handlerFixture](ctx, system, "session-a", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	ask := func(input any) any {
		t.Helper()
		output, err := system.AskGrain(ctx, target, input, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return output
	}
	advertisement := Advertise{
		ID:        "owner-a",
		SessionID: "session-a",
		Definition: commands.Definition{
			Name:        "compact",
			Description: "builtin",
		},
		Handler: endpoint,
	}
	first := ask(advertisement).(Registered)
	repeated := ask(advertisement).(Registered)
	if first.Err != nil || repeated.Err != nil || repeated.ID != first.ID || repeated.Registration.ID != first.Registration.ID {
		t.Fatalf("retry: %#v %#v", first, repeated)
	}
	advertisement.Definition.Description = "conflicting"
	if result := ask(advertisement).(Registered); result.Err == nil {
		t.Fatal("same ID changed publication content")
	}
	global := ask(Register{
		ID: "global",
		Definition: commands.Definition{
			Name:        "compact",
			Description: "configured",
		},
		Handler: target,
	}).(Registered)
	if global.Err != nil {
		t.Fatal(global.Err)
	}
	inherited := ask(Resolve{
		SessionID: "session-a",
		Name:      "compact",
	}).(Resolved)
	if !inherited.Found || !inherited.Handler.Equal(endpoint) || inherited.Registration.Definition.Description != "configured" {
		t.Fatalf("inheritance: %#v", inherited)
	}
	other := ask(Resolve{
		SessionID: "session-b",
		Name:      "compact",
	}).(Resolved)
	if !other.Found || !other.Handler.Equal(target) {
		t.Fatal("Session route leaked")
	}
	custom := ask(Register{
		ID:        "custom",
		SessionID: "session-a",
		Definition: commands.Definition{
			Name:        "compact",
			Description: "custom",
		},
		Handler: target,
	}).(Registered)
	if custom.Err != nil {
		t.Fatal(custom.Err)
	}
	if result := ask(List{
		SessionID: "session-a",
	}).(Descriptors); len(result.Values) != 1 || result.Values[0].Description != "custom" {
		t.Fatalf("override list: %#v", result)
	}
	ask(Unregister{
		ID: "custom",
	})
	restored := ask(Resolve{
		SessionID: "session-a",
		Name:      "compact",
	}).(Resolved)
	if !restored.Handler.Equal(endpoint) || restored.Registration.Definition.Description != "configured" {
		t.Fatal("withdrawing override lost inherited metadata or Session route")
	}
	ask(Unregister{
		ID: "owner-a",
	})
	advertisement.Definition.Description = "builtin"
	if result := ask(advertisement).(Registered); result.Err == nil {
		t.Fatal("delayed advertisement resurrected withdrawn route")
	}
	restored = ask(Resolve{
		SessionID: "session-a",
		Name:      "compact",
	}).(Resolved)
	if !restored.Handler.Equal(target) {
		t.Fatal("withdrawing Session route did not restore global route")
	}
}
