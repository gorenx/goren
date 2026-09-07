package spike

import (
	"context"
	"testing"
	"time"

	"github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type epochOpen struct{}
type epochRetire struct{}
type epochCheck struct{ Epoch int }
type epochStatus struct {
	Epoch  int
	Active bool
}

// epochRegistry survives individual grain activations. This is an in-memory
// authority fixture, not persistent recovery or a distributed lease.
type epochRegistry struct{ current epochStatus }

func (*epochRegistry) PreStart(*actor.Context) error { return nil }
func (*epochRegistry) PostStop(*actor.Context) error { return nil }
func (owner *epochRegistry) Receive(rc *actor.ReceiveContext) {
	switch incoming := rc.Message().(type) {
	case epochOpen:
		owner.current.Epoch++
		owner.current.Active = true
		rc.Response(owner.current)
	case epochRetire:
		owner.current.Active = false
		rc.Response(owner.current)
	case epochCheck:
		rc.Response(owner.current.Active && incoming.Epoch == owner.current.Epoch)
	}
}

type guardedEffect struct{ Epoch int }
type guardSnapshot struct{ Effects int }

// GuardedGrain activation acquires no effectful resources. The shared registry
// must authorize a generation before this fixture executes any operation.
type GuardedGrain struct{ effects int }

func (*GuardedGrain) OnActivate(context.Context, *actor.GrainProps) error   { return nil }
func (*GuardedGrain) OnDeactivate(context.Context, *actor.GrainProps) error { return nil }
func (owner *GuardedGrain) OnReceive(gc *actor.GrainContext) {
	switch incoming := gc.Message().(type) {
	case guardedEffect:
		// Reactivation does not retain GrainOf's request policy in this path.
		if failure := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); failure != nil {
			gc.Err(failure)
			return
		}
		reply := gc.DeferResponse()
		gc.RequestActor("epoch-registry", epochCheck{
			Epoch: incoming.Epoch,
		}, actor.WithRequestTimeout(time.Second)).Then(func(payload any, failure error) {
			if failure != nil {
				reply.Err(failure)
				return
			}
			if !payload.(bool) {
				reply.Response("stale-generation")
				return
			}
			owner.effects++
			reply.Response("executed")
		})
	case requestProbe:
		gc.Response(guardSnapshot{
			Effects: owner.effects,
		})
	}
}
func TestRetiredIdentityReactivationCannotExecuteOldGeneration(t *testing.T) {
	fixture := newRequestHarness(t)
	registryPID := spawnRequestActor(t, fixture.system, "epoch-registry", &epochRegistry{})
	identity, failure := actor.GrainOf[*GuardedGrain](context.Background(), fixture.system, "agent42-tool",
		actor.WithLongLivedGrain(),
		actor.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if failure != nil {
		t.Fatal(failure)
	}
	first := ask(t, registryPID, epochOpen{}).(epochStatus)
	assertPayload(t, asyncGrainCall(t, fixture.system, "old-call", identity, guardedEffect{
		Epoch: first.Epoch,
	}), "executed")
	ask(t, registryPID, epochRetire{})
	if failure = fixture.system.TellGrain(context.Background(), identity, new(actor.PoisonPill)); failure != nil {
		t.Fatal(failure)
	}
	deadline := time.Now().Add(time.Second)
	for {
		present := false
		for _, active := range fixture.system.Grains(context.Background(), time.Second) {
			if active.String() == identity.String() {
				present = true
			}
		}
		if !present {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("old grain not deactivated")
		}
		time.Sleep(time.Millisecond)
	}
	// This call actually reactivates the old virtual identity, but cannot acquire
	// authorization. No old operation is allowed to initialize an effectful tool.
	assertPayload(t, asyncGrainCall(t, fixture.system, "stale-call", identity, guardedEffect{
		Epoch: first.Epoch,
	}), "stale-generation")
	if observed := inspectGrain(t, fixture.system, identity, requestProbe{}).(guardSnapshot); observed.Effects != 0 {
		t.Fatal("reactivation executed stale effect")
	}
	second := ask(t, registryPID, epochOpen{}).(epochStatus)
	assertPayload(t, asyncGrainCall(t, fixture.system, "still-stale", identity, guardedEffect{
		Epoch: first.Epoch,
	}), "stale-generation")
	assertPayload(t, asyncGrainCall(t, fixture.system, "new-call", identity, guardedEffect{
		Epoch: second.Epoch,
	}), "executed")
	if observed := inspectGrain(t, fixture.system, identity, requestProbe{}).(guardSnapshot); observed.Effects != 1 {
		t.Fatalf("wrong effect count: %+v", observed)
	}
}
