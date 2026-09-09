package toolactor_test

import (
	"context"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	toolregistry "github.com/gorenx/goren/tools/registry"
	toolactor "github.com/gorenx/goren/tools/registry/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

func startRegistry(t *testing.T, pubsub bool) (goakt.ActorSystem, *goakt.GrainIdentity) {
	t.Helper()
	options := []goakt.Option{goakt.WithLogger(log.DiscardLogger)}
	if pubsub {
		options = append(options, goakt.WithPubSub())
	}
	system, err := goakt.NewActorSystem("schema-registry", options...)
	if err != nil {
		t.Fatal(err)
	}
	err = system.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		failure := system.Stop(context.Background())
		if failure != nil {
			t.Error(failure)
		}
	})
	identity, err := goakt.GrainOf[*toolactor.RegistryGrain](context.Background(), system, toolactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	return system, identity
}

func ask[T any](t *testing.T, system goakt.ActorSystem, target *goakt.GrainIdentity, input any) T {
	t.Helper()
	payload, err := system.AskGrain(context.Background(), target, input, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	value, valid := payload.(T)
	if !valid {
		t.Fatalf("unexpected reply %T", payload)
	}
	return value
}

func TestSchemaRegistrationRetainsConfigurationWhenNotificationFails(t *testing.T) {
	for _, pubsub := range []bool{false, true} {
		system, target := startRegistry(t, pubsub)
		reply := ask[toolactor.Completion](t, system, target, toolactor.RegisterSource{
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
		if (reply.Err == nil) != pubsub {
			t.Fatalf("notification=%v pubsub=%v", reply.Err, pubsub)
		}
		captured := ask[toolactor.Configuration](t, system, target, toolactor.ReadConfiguration{SessionID: "agent"})
		if len(captured.Value.Global.Schemas) != 1 || len(captured.Value.Custom.Schemas) != 0 {
			t.Fatal("configuration was lost or pre-merged")
		}
	}
}
