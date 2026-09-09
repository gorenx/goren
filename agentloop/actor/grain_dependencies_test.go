package loopactor_test

import (
	"context"
	agentactor "github.com/gorenx/goren/agent/actor"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	llmactor "github.com/gorenx/goren/llm/actor"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
	"testing"
	"time"

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

// executionRoutes supplies real transport endpoints. These admission tests hold
// execution in maintenance, so they do not require a configured model or tools.
type executionRoutes struct {
	router     *goakt.PID
	middleware *goakt.GrainIdentity
}

func executionDependencies(t *testing.T, system goakt.ActorSystem) executionRoutes {
	t.Helper()
	ctx := context.Background()
	catalog, err := goakt.GrainOf[*llmactor.Grain](ctx, system, "test-llm", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	pool, err := llmactor.SpawnPool(ctx, system, "test-model-pool", 1, llmactor.InitializeWorker{LLM: catalog})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := goakt.GrainOf[*middlewareactor.Grain](ctx, system, "test-middleware",
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
	)
	if err != nil {
		t.Fatal(err)
	}
	return executionRoutes{
		router:     pool,
		middleware: coordinator,
	}
}

func toolRuntimeRoute(t *testing.T, system goakt.ActorSystem, name string) *goakt.GrainIdentity {
	t.Helper()
	target, err := goakt.GrainOf[*runtimeactor.Runtime](context.Background(), system, runtimeactor.RuntimeName(name), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// pendingMaintenance acknowledges cancellation without claiming the original
// maintenance operation has completed; lifecycle tests release it explicitly.
type pendingMaintenance struct{}

func (*pendingMaintenance) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*pendingMaintenance) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (*pendingMaintenance) OnReceive(gc *goakt.GrainContext) {
	if _, valid := gc.Message().(agentactor.CancelMaintenance); valid {
		gc.Response(agentactor.MaintenanceCancelled{})
		return
	}
	gc.Unhandled()
}

func holdExecution(t *testing.T, system goakt.ActorSystem, target *goakt.GrainIdentity) {
	t.Helper()
	worker, err := goakt.GrainOf[*pendingMaintenance](context.Background(), system, "pending-maintenance", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	result, err := system.AskGrain(context.Background(), target, agentactor.BeginMaintenance{
		CreationID: "current",
		RequestID:  "test-maintenance",
		Worker:     worker,
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.(agentactor.MaintenanceAccepted).Err != nil {
		t.Fatal(result)
	}
}
