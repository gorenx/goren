package runtimeactor_test

import (
	"context"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

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

func TestCatalogReturnsUnmergedGlobalAndCustomSchemas(t *testing.T) {
	system, target, endpoint := startCatalog(t, true)
	reply := ask[catalogactor.Completion](t, system, target, catalogactor.RegisterSource{
		Name: "local",
		Catalog: toolregistry.Catalog{
			Schemas: []llm.ToolSchema{
				{
					Name:       "read",
					Parameters: []byte(`{"type":"object"}`),
				},
			},
		},
	})
	if reply.Err != nil {
		t.Fatal(reply.Err)
	}
	hidden := false
	err := endpoint.ConfigureSession(context.Background(), "agent", toolregistry.Catalog{
		Policies: map[string]toolregistry.Policy{
			"read": {Visible: &hidden},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	configured, err := endpoint.Read(context.Background(), "agent")
	if err != nil || len(configured.Global.Schemas) != 1 || len(configured.Custom.Schemas) != 0 || *configured.Custom.Policies["read"].Visible {
		t.Fatalf("configuration=%+v error=%v", configured, err)
	}
}
