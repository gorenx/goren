package loopactor_test

import (
	"context"
	"testing"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func sessionRoute(t *testing.T, system goakt.ActorSystem, name string) *goakt.GrainIdentity {
	t.Helper()
	identity, err := goakt.GrainOf[*sessionactor.Grain](context.Background(), system, sessionactor.RuntimeName(name), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func promptRoute(t *testing.T, system goakt.ActorSystem, name string, dependencies *goakt.GrainIdentity) *goakt.GrainIdentity {
	t.Helper()
	identity, err := goakt.GrainOf[*promptactor.RuntimeGrain](context.Background(), system, promptactor.RuntimeName(name), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	driver, replies := grainDriver(t, system, identity)
	if result := request[promptactor.RuntimeView](t, driver, replies, promptactor.ReadRuntime{}); result.Err == nil {
		return identity
	}
	if result := request[promptactor.RuntimeInitialized](t, driver, replies, promptactor.InitializeRuntime{
		AgentID:        session.SessionID(name),
		PromptRegistry: dependencies,
		ToolsRegistry:  dependencies,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	return identity
}
