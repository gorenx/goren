package spike

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/tochemey/goakt/v4/actor"
	gerrors "github.com/tochemey/goakt/v4/errors"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type grainRequestStart struct {
	Target  *actor.GrainIdentity
	Payload any
	Timeout time.Duration
}
type grainRun struct{ Chain []*actor.GrainIdentity }
type grainFinish struct{ Failure bool }
type grainConfigure struct{ Stopped chan<- string }
type grainObservation struct {
	Started   bool
	Finished  bool
	Cancelled bool
	Results   int
}

// DeferredGrain is an in-memory protocol fixture. A finish command stands for
// an I/O completion; it does not simulate durable storage or real tool effects.
// Only GrainReply survives OnReceive, never GrainContext.
type DeferredGrain struct {
	pending *actor.GrainReply
	state   grainObservation
	stopped chan<- string
	name    string
}

func (owner *DeferredGrain) OnActivate(_ context.Context, props *actor.GrainProps) error {
	owner.name = props.Identity().String()
	return nil
}
func (owner *DeferredGrain) OnDeactivate(context.Context, *actor.GrainProps) error {
	if owner.stopped != nil {
		owner.stopped <- owner.name
	}
	return nil
}
func (owner *DeferredGrain) OnReceive(gc *actor.GrainContext) {
	switch incoming := gc.Message().(type) {
	case grainConfigure:
		owner.stopped = incoming.Stopped
		gc.Response(true)
	case grainRun:
		if owner.state.Started {
			gc.Response("duplicate-operation")
			return
		}
		owner.state.Started = true
		owner.pending = gc.DeferResponse()
		if len(incoming.Chain) != 0 {
			remaining := grainRun{Chain: incoming.Chain[1:]}
			upstream := owner.pending
			gc.RequestGrain(incoming.Chain[0], remaining, actor.WithRequestTimeout(2*time.Second)).Then(func(payload any, failure error) {
				owner.state.Finished = true
				owner.state.Results++
				if failure != nil {
					upstream.Err(failure)
					return
				}
				upstream.Response("finalized:" + payload.(string))
			})
		}
	case grainFinish:
		if owner.pending == nil {
			gc.Response(false)
			return
		}
		owner.state.Finished = true
		// Exercise one-shot replies even when a duplicate completion is delivered.
		if incoming.Failure {
			owner.pending.Err(errors.New("tool-failed"))
		} else {
			owner.pending.Response("body")
		}
		owner.pending.Response("duplicate-must-not-win")
		gc.Response(true)
	case requestBusinessCancel:
		owner.state.Cancelled = true
		gc.Response("cancel-accepted")
	case requestProbe:
		gc.Response(owner.state)
	}
}

func makeDeferredGrain(t *testing.T, running actor.ActorSystem, name string) *actor.GrainIdentity {
	t.Helper()
	identity, failure := actor.GrainOf[*DeferredGrain](context.Background(), running, name,
		actor.WithLongLivedGrain(),
		actor.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if failure != nil {
		t.Fatal(failure)
	}
	return identity
}

// AskGrain is confined to the external test driver for observation/control.
func inspectGrain(t *testing.T, running actor.ActorSystem, identity *actor.GrainIdentity, payload any) any {
	t.Helper()
	observed, failure := running.AskGrain(context.Background(), identity, payload, time.Second)
	if failure != nil {
		t.Fatal(failure)
	}
	return observed
}
func awaitGrainStarted(t *testing.T, running actor.ActorSystem, identity *actor.GrainIdentity) grainObservation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		observed := inspectGrain(t, running, identity, requestProbe{}).(grainObservation)
		if observed.Started {
			return observed
		}
		if time.Now().After(deadline) {
			t.Fatal("grain not started")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestGrainDeferredRequestChain(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			fixture := newRequestHarness(t)
			agentID := makeDeferredGrain(t, fixture.system, "agent")
			runtimeID := makeDeferredGrain(t, fixture.system, "runtime")
			toolID := makeDeferredGrain(t, fixture.system, "tool")
			sendRequestMessage(t, fixture.client, grainRequestStart{
				Target:  agentID,
				Payload: grainRun{Chain: []*actor.GrainIdentity{runtimeID, toolID}},
				Timeout: 3 * time.Second,
			})
			awaitGrainStarted(t, fixture.system, toolID)
			for _, identity := range []*actor.GrainIdentity{agentID, runtimeID, toolID} {
				observed := inspectGrain(t, fixture.system, identity, requestProbe{}).(grainObservation)
				if !observed.Started || observed.Finished {
					t.Fatalf("not responsive while pending: %+v", observed)
				}
			}
			if count := ask(t, fixture.client, requestProbe{}); count != 0 {
				t.Fatal("premature reply")
			}
			inspectGrain(t, fixture.system, toolID, grainFinish{Failure: failed})
			outcome := readRequestOutcome(t, fixture.outcomes)
			if failed {
				if outcome.Err == nil || outcome.Err.Error() != "tool-failed" {
					t.Fatalf("failure lost: %+v", outcome)
				}
			} else if outcome.Err != nil || outcome.Payload != "finalized:finalized:body" {
				t.Fatalf("chain failed: %+v", outcome)
			}
			inspectGrain(t, fixture.system, toolID, grainFinish{})
			for _, identity := range []*actor.GrainIdentity{agentID, runtimeID} {
				observed := inspectGrain(t, fixture.system, identity, requestProbe{}).(grainObservation)
				if observed.Results != 1 || !observed.Finished {
					t.Fatalf("settlement: %+v", observed)
				}
			}
			if count := ask(t, fixture.client, requestProbe{}); count != 1 {
				t.Fatalf("duplicate reply: %v", count)
			}
		})
	}
}
func TestGrainCancellationAndLateReply(t *testing.T) {
	fixture := newRequestHarness(t)
	toolID := makeDeferredGrain(t, fixture.system, "cancel-tool")
	sendRequestMessage(t, fixture.client, grainRequestStart{
		Target:  toolID,
		Payload: grainRun{},
		Timeout: 2 * time.Second,
	})
	awaitGrainStarted(t, fixture.system, toolID)
	ask(t, fixture.client, requestCancel{})
	outcome := readRequestOutcome(t, fixture.outcomes)
	if !errors.Is(outcome.Err, gerrors.ErrRequestCanceled) {
		t.Fatalf("cancel: %+v", outcome)
	}
	observed := inspectGrain(t, fixture.system, toolID, requestProbe{}).(grainObservation)
	if observed.Cancelled || observed.Finished {
		t.Fatalf("cancel changed effect: %+v", observed)
	}
	inspectGrain(t, fixture.system, toolID, requestBusinessCancel{})
	observed = inspectGrain(t, fixture.system, toolID, requestProbe{}).(grainObservation)
	if !observed.Cancelled || observed.Finished {
		t.Fatalf("cancel acknowledgement is not completion: %+v", observed)
	}
	inspectGrain(t, fixture.system, toolID, grainFinish{})
	// A new async request proves the grain remains queryable after the old
	// deferred response is delivered to an already-cancelled correlation.
	sendRequestMessage(t, fixture.client, grainRequestStart{
		Target:  toolID,
		Payload: requestProbe{},
		Timeout: time.Second,
	})
	outcome = readRequestOutcome(t, fixture.outcomes)
	if outcome.Err != nil || !outcome.Payload.(grainObservation).Finished {
		t.Fatalf("final state: %+v", outcome)
	}
	if count := ask(t, fixture.client, requestProbe{}); count != 2 {
		t.Fatalf("late reply invoked old callback: %v", count)
	}
}
func TestGrainPoisonPillRequestAndExplicitDeactivation(t *testing.T) {
	fixture := newRequestHarness(t)
	agentID := makeDeferredGrain(t, fixture.system, "session-42-agent")
	sessionID := makeDeferredGrain(t, fixture.system, "session-42-session")
	stopped := make(chan string, 4)
	for _, identity := range []*actor.GrainIdentity{agentID, sessionID} {
		inspectGrain(t, fixture.system, identity, grainConfigure{Stopped: stopped})
	}
	inspectGrain(t, fixture.system, agentID, requestBusinessCancel{})
	// A Request envelope does not dispatch PoisonPill as a system stop.
	sendRequestMessage(t, fixture.client, grainRequestStart{
		Target:  agentID,
		Payload: new(actor.PoisonPill),
		Timeout: 100 * time.Millisecond,
	})
	outcome := readRequestOutcome(t, fixture.outcomes)
	if !errors.Is(outcome.Err, gerrors.ErrRequestTimeout) {
		t.Fatalf("PoisonPill Request: %+v", outcome)
	}
	select {
	case <-stopped:
		t.Fatal("Request unexpectedly invoked OnDeactivate")
	default:
	}
	names := []string{}
	for _, identity := range []*actor.GrainIdentity{agentID, sessionID} {
		if failure := fixture.system.TellGrain(context.Background(), identity, new(actor.PoisonPill)); failure != nil {
			t.Fatal(failure)
		}
		select {
		case name := <-stopped:
			names = append(names, name)
		case <-time.After(time.Second):
			t.Fatal("OnDeactivate not observed")
		}
	}
	if !reflect.DeepEqual(names, []string{agentID.String(), sessionID.String()}) {
		t.Fatalf("stop order: %v", names)
	}
	deadline := time.Now().Add(time.Second)
	for {
		active := fixture.system.Grains(context.Background(), time.Second)
		found := false
		for _, identity := range active {
			if identity.String() == agentID.String() {
				found = true
			}
		}
		if !found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("grain remained active after OnDeactivate")
		}
		time.Sleep(time.Millisecond)
	}
	// A stale identity can activate a fresh owner; long-lived is not a tombstone.
	observed := inspectGrain(t, fixture.system, agentID, requestProbe{}).(grainObservation)
	if observed.Started || observed.Finished || observed.Cancelled {
		t.Fatalf("unexpected restored state: %+v", observed)
	}
}
