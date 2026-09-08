package registry_test

import (
	"context"
	"errors"
	"testing"

	configactor "github.com/gorenx/goren/llm/actor"
	"github.com/gorenx/goren/systemprompt"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	"github.com/gorenx/goren/tools"
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
	case toolactor.Read:
		gc.Response(toolactor.Catalog{
			Found: true,
			Definitions: tools.DefinitionsChanged{
				AgentID: input.AgentID,
				Version: 1,
			},
		})
	default:
		gc.Unhandled()
	}
}

func configurationRoutes(t *testing.T, system goakt.ActorSystem) *goakt.GrainIdentity {
	t.Helper()
	identity, err := goakt.GrainOf[*configurationOwner](context.Background(), system, "configuration", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	return identity
}
