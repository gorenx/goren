package runtimeactor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/toolaskuser"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	"github.com/gorenx/goren/tools/tool"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func TestSupervisorOwnsRuntimeStartupAndRetryableClosure(t *testing.T) {
	provider := &source{}
	system, registry, _ := startCatalog(t, true, tool.Providers{
		toolaskuser.Name: provider,
	})
	authority, err := goakt.GrainOf[*runtimeAuthority](t.Context(), system, "authority", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	selected := toolregistry.Catalog{
		Schemas: []llm.ToolSchema{toolaskuser.Schema()},
	}
	sender, err := runtimeactor.NewSupervisor(t.Context(), system, runtimeactor.InitializeRuntime{
		AgentID:    "agent",
		CreationID: "creation",
		Authority:  authority,
		Registry:   registry,
		Config:     &selected,
		Timeout:    time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Construction freezes input but does not create implementations.
	selected.Schemas[0].Name = "mutated"
	if provider.opens.Load() != 0 {
		t.Fatal("Supervisor construction opened tools")
	}
	if err := startClient(t, system, sender, t.Context()); err != nil {
		t.Fatal(err)
	}
	definitions := ask[runtimeactor.Definitions](t, system, sender.Runtime(), runtimeactor.ReadDefinitions{
		CreationID: "creation",
	})
	if definitions.Err != nil || len(definitions.Value.Schemas) != 1 || definitions.Value.Schemas[0].Name != toolaskuser.Name {
		t.Fatalf("frozen catalog not installed: %+v", definitions)
	}
	identifier := ask[string](t, system, authority, readClaim{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := closeClient(t, system, sender, ctx, identifier); !errors.Is(err, context.Canceled) || provider.closes.Load() != 0 {
		t.Fatal("canceled close released resources", err)
	}
	if err := closeClient(t, system, sender, t.Context(), "wrong-activation"); err == nil || provider.closes.Load() != 0 {
		t.Fatal("wrong activation closed tools", err)
	}
	provider.closeFailure.Store(true)
	if err := closeClient(t, system, sender, t.Context(), identifier); err == nil || provider.closes.Load() != 1 {
		t.Fatal("failed resource release was acknowledged", err)
	}
	provider.closeFailure.Store(false)
	if err := closeClient(t, system, sender, t.Context(), identifier); err != nil {
		t.Fatal(err)
	}
	if err := closeClient(t, system, sender, t.Context(), identifier); err != nil {
		t.Fatal("repeated close", err)
	}
	if provider.opens.Load() != 1 || provider.closes.Load() != 2 {
		t.Fatal("retry recreated or repeatedly closed tools")
	}
	address := ask[catalogactor.ToolAddress](t, system, registry, catalogactor.FindTool{
		AgentID: "agent",
		Name:    toolaskuser.Name,
	})
	if address.Found {
		t.Fatal("closed tool remains callable")
	}
	if err := startClient(t, system, sender, t.Context()); err == nil {
		t.Fatal("closed Supervisor reopened its runtime")
	}
}

func TestSupervisorClosesUnstartedRuntimeWithoutClaim(t *testing.T) {
	system, registry, _ := startCatalog(t, true, nil)
	sender, err := runtimeactor.NewSupervisor(t.Context(), system, runtimeactor.InitializeRuntime{
		AgentID:    "unstarted",
		CreationID: "creation",
		Registry:   registry,
		Timeout:    time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := closeClient(t, system, sender, t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if err := closeClient(t, system, sender, t.Context(), ""); err != nil {
		t.Fatal(err)
	}
}

// clientCaller supplies the same live GrainContext as the production Registry.
// It owns no Runtime lifecycle decisions; those stay in the real Supervisor.
type clientCaller struct{}

type startRequest struct {
	sender *runtimeactor.Supervisor
}

type closeRequest struct {
	sender     *runtimeactor.Supervisor
	instanceID string
}

type clientResult struct {
	reply *goakt.GrainReply
	err   error
}

func (*clientCaller) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*clientCaller) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (*clientCaller) OnReceive(gc *goakt.GrainContext) {
	var completed <-chan error
	switch input := gc.Message().(type) {
	case startRequest:
		completed = input.sender.Start(gc)
	case closeRequest:
		completed = input.sender.Close(gc, input.instanceID)
	case clientResult:
		input.reply.Response(runtimeactor.Completion{
			Err: input.err,
		})
		return
	default:
		gc.Unhandled()
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(runtimeactor.Completion{
			Err: errors.New("test: caller requires an async request"),
		})
		return
	}
	if err := gc.PipeToSelf(func() (any, error) {
		return clientResult{
			reply: reply,
			err:   <-completed,
		}, nil
	}); err != nil {
		reply.Response(runtimeactor.Completion{
			Err: err,
		})
	}
}

func startClient(t *testing.T, system goakt.ActorSystem, sender *runtimeactor.Supervisor, ctx context.Context) error {
	t.Helper()
	return callClient(t, system, ctx, startRequest{
		sender: sender,
	})
}

func closeClient(t *testing.T, system goakt.ActorSystem, sender *runtimeactor.Supervisor, ctx context.Context, instanceID string) error {
	t.Helper()
	return callClient(t, system, ctx, closeRequest{
		sender:     sender,
		instanceID: instanceID,
	})
}

func callClient(t *testing.T, system goakt.ActorSystem, ctx context.Context, input any) error {
	t.Helper()
	if err := ctx.Err(); err != nil {
		return err
	}
	address, err := goakt.GrainOf[*clientCaller](ctx, system, "lifecycle-caller",
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
	)
	if err != nil {
		return err
	}
	payload, err := system.AskGrain(ctx, address, input, 3*time.Second)
	if err != nil {
		return err
	}
	result, ok := payload.(runtimeactor.Completion)
	if !ok {
		t.Fatalf("unexpected lifecycle result: %T", payload)
	}
	return result.Err
}

func TestSupervisorInitializationDoesNotDependOnTargetActivationOptions(t *testing.T) {
	for _, scenario := range []string{"preactivated", "reactivated"} {
		t.Run(scenario, func(t *testing.T) {
			provider := &source{}
			system, registry, _ := startCatalog(t, true, tool.Providers{
				toolaskuser.Name: provider,
			})
			// Deliberately omit reentrancy. Another caller may obtain the route
			// before the Supervisor, or a delayed message may reactivate it.
			address, err := goakt.GrainOf[*runtimeactor.Runtime](t.Context(), system,
				runtimeactor.RuntimeName("agent"), goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			toolAddress, err := goakt.GrainOf[*toolactor.Grain](t.Context(), system,
				toolactor.RuntimeName("agent", toolaskuser.Name), goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}

			if scenario == "reactivated" {
				if err := system.TellGrain(t.Context(), toolAddress, new(goakt.PoisonPill)); err != nil {
					t.Fatal(err)
				}
				if err := system.TellGrain(t.Context(), address, new(goakt.PoisonPill)); err != nil {
					t.Fatal(err)
				}
			}
			described := ask[toolactor.Description](t, system, toolAddress, toolactor.Describe{
				CreationID: "previous",
			})
			if described.Err == nil {
				t.Fatal("uninitialized tool exposed a descriptor")
			}

			stale := ask[runtimeactor.Definitions](t, system, address, runtimeactor.ReadDefinitions{
				CreationID: "previous",
			})
			if stale.Err == nil {
				t.Fatal("uninitialized activation exposed definitions")
			}
			authority, err := goakt.GrainOf[*runtimeAuthority](t.Context(), system, uuid.NewString(), goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			sender, err := runtimeactor.NewSupervisor(t.Context(), system, runtimeactor.InitializeRuntime{
				AgentID:    "agent",
				CreationID: "current",
				Authority:  authority,
				Registry:   registry,
				Config: &toolregistry.Catalog{
					Schemas: []llm.ToolSchema{toolaskuser.Schema()},
				},
				Timeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := closeClient(t, system, sender, context.Background(), ask[string](t, system, authority, readClaim{})); err != nil {
					t.Error(err)
				}
			})
			if err := startClient(t, system, sender, t.Context()); err != nil {
				t.Fatal(err)
			}
			current := ask[runtimeactor.Definitions](t, system, address, runtimeactor.ReadDefinitions{
				CreationID: "current",
			})
			if current.Err != nil || len(current.Value.Schemas) != 1 || provider.opens.Load() != 1 {
				t.Fatalf("current tools not initialized: %+v", current)
			}
			stale = ask[runtimeactor.Definitions](t, system, address, runtimeactor.ReadDefinitions{
				CreationID: "previous",
			})
			if stale.Err == nil {
				t.Fatal("previous creation accessed current tools")
			}
		})
	}
}
