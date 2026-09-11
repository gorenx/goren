package server

import (
	"context"
	"errors"
	"github.com/gorenx/goren/llm"
	configactor "github.com/gorenx/goren/llm/actor"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/llm/modelconfig"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
	"testing"
	"time"
)

// resolveRequest uses a named cross-actor contract rather than a generic payload.
type resolveRequest struct {
	Target *goakt.GrainIdentity
}

type configConsumer struct {
	results chan configactor.AgentConfiguration
}

func (*configConsumer) PreStart(*goakt.Context) error { return nil }
func (*configConsumer) PostStop(*goakt.Context) error { return nil }
func (consumer *configConsumer) Receive(rc *goakt.ReceiveContext) {
	if incoming, ok := rc.Message().(resolveRequest); ok {
		rc.RequestGrain(incoming.Target, configactor.ResolveAgent{}, goakt.WithRequestTimeout(time.Second)).Then(func(payload any, err error) {
			if err != nil {
				consumer.results <- configactor.AgentConfiguration{
					Err: err,
				}
				return
			}
			response, ok := payload.(configactor.AgentConfiguration)
			if !ok {
				response.Err = errors.New("test: unexpected resolution response")
			}
			consumer.results <- response
		})
	}
}

func TestModelConfigCompositionAndAsyncRequest(t *testing.T) {
	ctx := context.Background()
	system, err := goakt.NewActorSystem("configuration-test", goakt.WithLogger(log.DiscardLogger))
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
	endpoint, err := initializeModels(ctx, system, DefaultModelConfig())
	if err != nil {
		t.Fatal(err)
	}
	initial, err := endpoint.CurrentConfig(ctx)
	if err != nil || initial.Definition.Default.Provider != deepseek.ProviderRoute || initial.Definition.Default.Model != deepseek.DefaultModelID {
		t.Fatal("default configuration changed")
	}
	candidate := initial.Definition
	candidate.Default.Model = "next-model"
	if _, err := endpoint.Replace(ctx, 1, candidate); err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.Replace(ctx, 1, candidate); !errors.Is(err, modelconfig.ErrVersion) {
		t.Fatalf("business error chain lost: %v", err)
	}
	identity, err := goakt.GrainOf[*configactor.Grain](ctx, system, configactor.Name, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan configactor.AgentConfiguration, 1)
	pid, err := system.Spawn(ctx, "config-consumer", &configConsumer{
		results: results,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	if err := goakt.Tell(ctx, pid, resolveRequest{
		Target: identity,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-results:
		if response.Err != nil || response.Resolved.Version != 2 || response.Resolved.Config.Model != "next-model" || response.Resolved.CredentialRef != deepseek.DefaultAPIKeyEnv {
			t.Fatal("async configuration request failed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("async configuration response timed out")
	}
	if initial.Definition.Default.Model != deepseek.DefaultModelID {
		t.Fatal("update mutated previous response")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := endpoint.Resolve(cancelled, 0, llm.CallConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}
