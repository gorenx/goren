package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

var unfinishedWork = errors.New("accepted work remains")

// closeGuard isolates a retryable execution failure from Registry orchestration.
type closeGuard struct {
	attempts int
}

func (*closeGuard) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*closeGuard) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *closeGuard) OnReceive(gc *goakt.GrainContext) {
	switch gc.Message().(type) {
	case agentactor.PrepareClose:
		owner.attempts++
		var failure error
		if owner.attempts == 1 {
			failure = unfinishedWork
		}
		gc.Response(agentactor.Prepared{
			Err: failure,
		})
	case agentactor.CloseResources:
		gc.Response(agentactor.Closed{})
	default:
		gc.Unhandled()
	}
}

func TestCloseAgentsRetainsFailedRootsAndRejectsNewAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	system, err := goakt.NewActorSystem("server-agent-close", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer system.Stop(context.Background())
	registry, err := goakt.GrainOf[*agentregistry.Grain](ctx, system, agentregistry.Name,
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	ask := func(input any) any {
		t.Helper()
		result, err := system.AskGrain(ctx, registry, input, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	parent := agentregistry.Creation{
		AgentID:    "b",
		CreationID: "b-current",
	}
	for _, input := range []agentregistry.ReserveAgent{
		{
			Creation: parent,
		},
		{
			Creation: agentregistry.Creation{
				AgentID:    "child",
				CreationID: "child-current",
			},
			Parent: &parent,
		},
		{
			Creation: agentregistry.Creation{
				AgentID:    "a",
				CreationID: "a-current",
			},
		},
	} {
		if err := ask(input).(agentregistry.Completion).Err; err != nil {
			t.Fatal(err)
		}
	}
	executor, err := goakt.GrainOf[*closeGuard](ctx, system, loopactor.RuntimeName("b"), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	claimed := ask(agentactor.Claim{
		AgentID:    "b",
		CreationID: "b-current",
		InstanceID: "instance",
		Executor:   executor,
	}).(agentactor.Claimed)
	if claimed.Err != nil {
		t.Fatal(claimed.Err)
	}
	roots := ask(agentregistry.StopAdmission{}).(agentregistry.Roots)
	if len(roots.Creations) != 2 || roots.Creations[0].AgentID != "a" || roots.Creations[1] != parent {
		t.Fatalf("shutdown inventory: %#v", roots)
	}
	for _, ancestor := range []*agentregistry.Creation{nil, &parent} {
		result := ask(agentregistry.ReserveAgent{
			Creation: agentregistry.Creation{
				AgentID:    "new",
				CreationID: "new-current",
			},
			Parent: ancestor,
		}).(agentregistry.Completion)
		if !errors.Is(result.Err, agent.ErrClosing) {
			t.Fatalf("new admission: %v", result.Err)
		}
	}
	if err := closeAgents(ctx, system, registry); !errors.Is(err, unfinishedWork) {
		t.Fatalf("unfinished execution: %v", err)
	}
	roots = ask(agentregistry.StopAdmission{}).(agentregistry.Roots)
	if len(roots.Creations) != 1 || roots.Creations[0] != parent {
		t.Fatalf("remaining responsibility: %#v", roots)
	}
	if err := closeAgents(ctx, system, registry); err != nil {
		t.Fatal(err)
	}
	roots = ask(agentregistry.StopAdmission{}).(agentregistry.Roots)
	if len(roots.Creations) != 0 {
		t.Fatalf("remaining roots: %#v", roots)
	}
	if err := closeAgents(ctx, system, registry); err != nil {
		t.Fatal(err)
	}
	if checked := ask(agentregistry.CheckAdmission{
		Creation: agentregistry.Creation{
			AgentID:    "child",
			CreationID: "child-current",
		},
	}).(agentregistry.Completion); !errors.Is(checked.Err, agentregistry.ErrCreation) {
		t.Fatalf("descendant reservation survived: %v", checked.Err)
	}
}
