package registry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	agentactor "github.com/gorenx/goren/agent/actor"
	configactor "github.com/gorenx/goren/llm/actor"
	"github.com/gorenx/goren/systemprompt"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	toolactor "github.com/gorenx/goren/tools/registry/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// configurationOwner supplies only configuration contracts. Tests retain real
// Agent, Session, Prompt Runtime and lifecycle actors around this fixture.
type configurationOwner struct {
	failure error
	held    chan *goakt.GrainReply
}

type failConfiguration struct{}

func (*configurationOwner) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*configurationOwner) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (fixture *configurationOwner) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case agentactor.CancelMaintenance:
		gc.Response(agentactor.MaintenanceCancelled{})
	case holdConfiguration:
		fixture.held = input.replies
		gc.Response(struct{}{})
	case failConfiguration:
		fixture.failure = errors.New("test: configuration unavailable")
		gc.Response(struct{}{})
	case configactor.InitializeAgent:
		if fixture.held != nil {
			fixture.held <- gc.DeferResponse()
			fixture.held = nil
			return
		}
		gc.Response(configactor.Completion{
			Err: fixture.failure,
		})
	case promptactor.Prepare:
		settings, err := systemprompt.ValidateConfig(systemprompt.Config{})
		if err != nil {
			panic(err)
		}
		definitions, err := systemprompt.NewLayer(settings)
		if err != nil {
			panic(err)
		}
		gc.Response(promptactor.Prepared{
			Version:  1,
			Snapshot: definitions.Capture(),
		})
	default:
		gc.Unhandled()
	}
}

func configurationRoutes(t *testing.T, system goakt.ActorSystem) *goakt.GrainIdentity {
	t.Helper()
	initializeToolRegistry(t, system)
	identity, err := goakt.GrainOf[*configurationOwner](context.Background(), system, "configuration", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func toolRegistry(t *testing.T, system goakt.ActorSystem) *goakt.GrainIdentity {
	t.Helper()
	identity, err := goakt.GrainOf[*toolactor.RegistryGrain](context.Background(), system, toolactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func initializeToolRegistry(t *testing.T, system goakt.ActorSystem) {
	t.Helper()
	payload, err := system.AskGrain(context.Background(), toolRegistry(t, system), toolactor.InitializeDefaults{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	initialized, valid := payload.(toolactor.Completion)
	if !valid || initialized.Err != nil {
		t.Fatalf("tool defaults: %v", payload)
	}
}
