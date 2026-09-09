package runtimeactor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type unopened struct{}

func (unopened) Open(context.Context) (tool.Implementation, error) {
	panic("catalog must not construct tools")
}

func startCatalog(t *testing.T, pubsub bool) (goakt.ActorSystem, *goakt.GrainIdentity, *runtimeactor.Catalog) {
	t.Helper()
	options := []goakt.Option{goakt.WithLogger(log.DiscardLogger)}
	if pubsub {
		options = append(options, goakt.WithPubSub())
	}
	system, err := goakt.NewActorSystem("tool-update-catalog", options...)
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
	target, err := goakt.GrainOf[*catalogactor.RegistryGrain](context.Background(), system, catalogactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := runtimeactor.NewCatalog(system, target, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return system, target, endpoint
}

func ask[T any](t *testing.T, system goakt.ActorSystem, target *goakt.GrainIdentity, message any) T {
	t.Helper()
	payload, err := system.AskGrain(context.Background(), target, message, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reply, ok := payload.(T)
	if !ok {
		t.Fatalf("unexpected reply %T", payload)
	}
	return reply
}

func TestCatalogPreservesPublicationWhenNotificationFails(t *testing.T) {
	for _, pubsub := range []bool{true, false} {
		name := "without-pubsub"
		if pubsub {
			name = "with-pubsub"
		}
		t.Run(name, func(t *testing.T) {
			system, target, endpoint := startCatalog(t, pubsub)
			ctx := context.Background()
			if _, err := endpoint.ReadConfiguration(ctx, "update"); err == nil {
				t.Fatal("missing update became an empty configuration")
			}
			if reply := ask[catalogactor.Completion](t, system, target, catalogactor.InitializeDefaults{
				Tools: []toolregistry.Tool{{
					Name:     "echo",
					Provider: unopened{},
					Visible:  true,
				}},
			}); reply.Err != nil {
				t.Fatal(reply.Err)
			}
			registered := ask[catalogactor.Configuration](t, system, target, catalogactor.RegisterConfiguration{
				ID:      "update",
				AgentID: "agent",
			})
			if !registered.Found || (registered.Err == nil) != pubsub {
				t.Fatalf("configuration = %#v", registered)
			}
			configured, err := endpoint.ReadConfiguration(ctx, "update")
			if err != nil || configured.Update.AgentID != "agent" || len(configured.Tools) != 1 {
				t.Fatalf("configuration read = %#v, %v", configured, err)
			}
			pending, found, err := endpoint.Lookup(ctx, "update")
			if err != nil || !found || pending.Published {
				t.Fatalf("unpublished lookup = %#v, %v, %v", pending, found, err)
			}
			if _, err := endpoint.Publish(ctx, "update"); err == nil {
				t.Fatal("incomplete publication accepted")
			}
			if reply := ask[catalogactor.Completion](t, system, target, catalogactor.RegisterDefinition{
				ID: "update",
				Candidate: toolregistry.Candidate{
					Schema: llm.ToolSchema{
						Name:       "echo",
						Parameters: []byte(`{"type":"object"}`),
					},
					Visible: true,
				},
			}); reply.Err != nil {
				t.Fatal(reply.Err)
			}
			fact, err := endpoint.Publish(ctx, "update")
			if len(fact.Schemas) != 1 || fact.Schemas[0].Version != 1 || (err == nil) != pubsub {
				t.Fatalf("publication lost committed data: %#v, %v", fact, err)
			}
			published, found, err := endpoint.Lookup(ctx, "update")
			if err != nil || !found || !published.Published || len(published.Catalog.Schemas) != 1 || published.Catalog.Schemas[0].Version != 1 {
				t.Fatalf("publication recovery = %#v, %v, %v", published, found, err)
			}
			if err := endpoint.Abort(ctx, "update"); err == nil {
				t.Fatal("published update was aborted")
			}
			if err := endpoint.Settle(ctx, "update"); err != nil {
				t.Fatal(err)
			}
			if _, found, err := endpoint.Lookup(ctx, "update"); err != nil || found {
				t.Fatalf("settled lookup = %v, %v", found, err)
			}
		})
	}
}

func TestCatalogRejectsCancelledRequestsWithoutChangingBatch(t *testing.T) {
	system, target, endpoint := startCatalog(t, true)
	if reply := ask[catalogactor.Completion](t, system, target, catalogactor.BeginUpdate{
		Update: toolregistry.Update{
			ID:      "retained",
			AgentID: "agent",
		},
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("caller cancelled")
	cancel(cause)
	if err := endpoint.Abort(ctx, "retained"); !errors.Is(err, cause) {
		t.Fatalf("cancellation = %v", err)
	}
	if _, found, err := endpoint.Lookup(context.Background(), "retained"); err != nil || !found {
		t.Fatal("cancelled request removed the batch")
	}
	if err := endpoint.Abort(context.Background(), "retained"); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogConfigurationPreservesRegistrationAndEmptyPublication(t *testing.T) {
	for _, pubsub := range []bool{false, true} {
		name := "notification-unavailable"
		if pubsub {
			name = "notification-available"
		}
		t.Run(name, func(t *testing.T) {
			system, target, endpoint := startCatalog(t, pubsub)
			ctx := context.Background()
			initialized := ask[catalogactor.Completion](t, system, target, catalogactor.InitializeDefaults{})
			if initialized.Err != nil {
				t.Fatal(initialized.Err)
			}
			baseline := ask[catalogactor.Catalog](t, system, target, catalogactor.ReadDefinitions{
				AgentID: "agent",
			})
			if baseline.Err != nil || baseline.Found {
				t.Fatalf("unpublished catalog: %#v", baseline)
			}
			configured, err := endpoint.Configure(ctx, "initial", "agent", nil)
			if (err == nil) != pubsub {
				t.Fatal("notification outcome lost", err)
			}
			if configured.Update.ID != "initial" || configured.Update.AgentID != "agent" {
				t.Fatal("registered configuration lost", configured)
			}
			recovered, err := endpoint.ReadConfiguration(ctx, "initial")
			if err != nil || recovered.Update.ID != configured.Update.ID {
				t.Fatal("configuration could not be recovered", err)
			}
			baseline = ask[catalogactor.Catalog](t, system, target, catalogactor.ReadDefinitions{
				AgentID: "agent",
			})
			if baseline.Err != nil || baseline.Found {
				t.Fatal("configuration registration prematurely published tools", baseline)
			}
			published, err := endpoint.Publish(ctx, "initial")
			if (err == nil) != pubsub || published.AgentID != "agent" || len(published.KnownNames) != 0 {
				t.Fatal("empty publication lost", published, err)
			}
			_, err = endpoint.Configure(ctx, "blocked", "agent", nil)
			if err == nil {
				t.Fatal("unsettled update bypassed")
			}
			err = endpoint.Settle(ctx, "initial")
			if err != nil {
				t.Fatal(err)
			}
			baseline = ask[catalogactor.Catalog](t, system, target, catalogactor.ReadDefinitions{
				AgentID: "agent",
			})
			if baseline.Err != nil || !baseline.Found || baseline.Definitions.AgentID != "agent" || len(baseline.Definitions.Schemas) != 0 {
				t.Fatalf("empty catalog treated as missing: %#v", baseline)
			}
			next, err := endpoint.Configure(ctx, "next", "agent", nil)
			if (err == nil) != pubsub || next.Update.ID != "next" {
				t.Fatal("settled update prevented next configuration", next, err)
			}
			err = endpoint.Abort(ctx, "next")
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
