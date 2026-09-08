package registry_test

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/agent/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
	"testing"
	"time"
)

type command struct {
	payload any
}

type response struct {
	value any
	err   error
}

type grainRequester struct {
	target  *goakt.GrainIdentity
	replies chan response
}

func (*grainRequester) PreStart(*goakt.Context) error { return nil }
func (*grainRequester) PostStop(*goakt.Context) error { return nil }
func (driver *grainRequester) Receive(rc *goakt.ReceiveContext) {
	if input, ok := rc.Message().(command); ok {
		replies := driver.replies
		rc.RequestGrain(driver.target, input.payload).Then(func(value any, err error) {
			replies <- response{
				value: value,
				err:   err,
			}
		})
	}
}
func grainDriver(t *testing.T, system goakt.ActorSystem, identity *goakt.GrainIdentity) (*goakt.PID, <-chan response) {
	t.Helper()
	replies := make(chan response, 4)
	driver, err := system.Spawn(context.Background(), "requester-"+uuid.NewString(), &grainRequester{
		target:  identity,
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	return driver, replies
}

func request[T any](t *testing.T, driver *goakt.PID, replies <-chan response, payload any) T {
	t.Helper()
	if err := goakt.Tell(context.Background(), driver, command{
		payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-replies:
		if result.err != nil {
			t.Fatal(result.err)
		}
		value, ok := result.value.(T)
		if !ok {
			t.Fatalf("unexpected reply %T", result.value)
		}
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("request timed out")
		var empty T
		return empty
	}
}

func TestRegistryRequestsPreserveCreationAndAncestorAdmission(t *testing.T) {
	ctx := context.Background()
	system, err := goakt.NewActorSystem("agent-registry-test", goakt.WithLogger(log.DiscardLogger))
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
	identity, err := goakt.GrainOf[*registry.Grain](ctx, system, registry.Name, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	driver, replies := grainDriver(t, system, identity)
	parent := registry.Creation{
		AgentID:    "parent",
		CreationID: "first",
	}
	child := registry.Creation{
		AgentID:    "child",
		CreationID: "child-first",
	}
	executor, err := goakt.GrainOf[*executorStub](ctx, system, "parent.agentloop", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}

	for _, input := range []registry.Reserve{
		{
			Creation: parent,
		},
		{
			Creation: parent,
		},
		{
			Creation: child,
			Parent:   &parent,
		},
	} {
		if result := request[registry.Completion](t, driver, replies, input); result.Err != nil {
			t.Fatal(result.Err)
		}
	}

	for _, instance := range []string{"activation-one", "activation-one"} {
		if result := request[agentactor.Claimed](t, driver, replies, agentactor.Claim{
			AgentID:    parent.AgentID,
			CreationID: parent.CreationID,
			InstanceID: instance,
			Executor:   executor,
		}); result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	for _, instance := range []string{"", "activation-two"} {
		if result := request[agentactor.Claimed](t, driver, replies, agentactor.Claim{
			AgentID:    parent.AgentID,
			CreationID: parent.CreationID,
			InstanceID: instance,
			Executor:   executor,
		}); !errors.Is(result.Err, registry.ErrInstance) {
			t.Fatalf("conflicting activation = %v", result.Err)
		}
	}
	if result := request[registry.CloseAccepted](t, driver, replies, registry.BeginClose{
		Creation: parent,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}

	for range 2 {
		result := request[registry.CloseAccepted](t, driver, replies, registry.BeginClose{
			Creation: parent,
		})
		if result.Err != nil || len(result.Children) != 1 || result.Children[0] != child {
			t.Fatalf("close children = %+v", result)
		}
		// A receiver may modify its reply without altering the Registry's entries.
		result.Children[0].CreationID = "modified"
	}

	if result := request[agentactor.Claimed](t, driver, replies, agentactor.Claim{
		AgentID:    child.AgentID,
		CreationID: child.CreationID,
		InstanceID: "late-child-activation",
	}); !errors.Is(result.Err, agent.ErrClosing) {
		t.Fatalf("initialization after ancestor close = %v", result.Err)
	}
	if result := request[registry.Completion](t, driver, replies, registry.CheckAdmission{
		Creation: child,
	}); !errors.Is(result.Err, agent.ErrClosing) {
		t.Fatalf("descendant admission = %v", result.Err)
	}
	if result := request[registry.Completion](t, driver, replies, registry.Reserve{
		Creation: registry.Creation{
			AgentID:    "grandchild",
			CreationID: "grandchild-first",
		},
		Parent: &child,
	}); !errors.Is(result.Err, agent.ErrClosing) {
		t.Fatalf("descendant creation = %v", result.Err)
	}
	for _, current := range []registry.Creation{child, parent} {
		if result := request[agentactor.Closed](t, driver, replies, registry.CloseAgent{
			Creation: current,
		}); result.Err != nil {
			t.Fatal(result.Err)
		}
		if current == child {
			result := request[registry.CloseAccepted](t, driver, replies, registry.BeginClose{
				Creation: parent,
			})
			if result.Err != nil || len(result.Children) != 0 {
				t.Fatalf("released child remains in close reply: %+v", result)
			}
		}
	}
	replacement := registry.Creation{
		AgentID:    parent.AgentID,
		CreationID: "second",
	}
	if result := request[registry.Completion](t, driver, replies, registry.Reserve{
		Creation: replacement,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	if result := request[agentactor.Claimed](t, driver, replies, agentactor.Claim{
		AgentID:    parent.AgentID,
		CreationID: parent.CreationID,
		InstanceID: "activation-one",
		Executor:   executor,
	}); !errors.Is(result.Err, registry.ErrCreation) {
		t.Fatal("old activation claimed replacement", result.Err)
	}
	if result := request[registry.CloseAccepted](t, driver, replies, registry.BeginClose{
		Creation: parent,
	}); !errors.Is(result.Err, registry.ErrCreation) || len(result.Children) != 0 {
		t.Fatalf("stale close = %+v", result)
	}
	for _, input := range []any{
		registry.Reserve{
			Creation: child,
			Parent:   &parent,
		},
	} {
		if result := request[registry.Completion](t, driver, replies, input); !errors.Is(result.Err, registry.ErrCreation) {
			t.Fatalf("stale operation = %v", result.Err)
		}
	}
	if result := request[agentactor.Claimed](t, driver, replies, agentactor.Claim{
		AgentID:    replacement.AgentID,
		CreationID: replacement.CreationID,
		InstanceID: "activation-two",
		Executor:   executor,
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	if result := request[registry.Completion](t, driver, replies, registry.CheckAdmission{
		Creation: replacement,
	}); result.Err != nil {
		t.Fatal("stale operation affected replacement", result.Err)
	}
}

// executorStub isolates directory rules from AgentLoop business execution.
type executorStub struct{}

func (*executorStub) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*executorStub) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (*executorStub) OnReceive(gc *goakt.GrainContext) {
	switch gc.Message().(type) {
	case agentactor.PrepareClose:
		gc.Response(agentactor.Prepared{})
	case agentactor.CloseResources:
		gc.Response(agentactor.Closed{})
	default:
		gc.Unhandled()
	}
}
