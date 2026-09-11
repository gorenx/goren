package spike

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tochemey/goakt/v4/actor"
	gerrors "github.com/tochemey/goakt/v4/errors"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type continuationStart struct {
	Target  *actor.GrainIdentity
	Payload any
	Next    any
	Timeout time.Duration
	Exited  chan<- struct{}
}

type continuationCancel struct{}
type continuationClose struct{}

// continuationOutcome is a local business response. Err travels as a Go value,
// outside the framework error envelope that serializes arbitrary errors.
type continuationOutcome struct {
	Payload any
	Err     error
}

type continuationResult struct {
	Activation string
	Sequence   uint64
	Payload    any
	Failure    error
}

type continuationState struct {
	Activation  string
	Pending     bool
	Closing     bool
	Settlements int
}

// continuation owns a single local wait, never a reference back to its Grain.
type continuation struct {
	sequence uint64
	cancel   context.CancelFunc
	request  actor.RequestCall
}

// ContinuationGrain tests a local two-stage request. It owns no business effects
// and cannot establish production close, authorization or durability semantics.
type ContinuationGrain struct {
	activation  string
	sequence    uint64
	active      *continuation
	upstream    *actor.GrainReply
	closing     *actor.GrainReply
	input       continuationStart
	cancelled   bool
	closed      bool
	settlements int
}

func (owner *ContinuationGrain) OnActivate(context.Context, *actor.GrainProps) error {
	owner.activation = uuid.NewString()
	return nil
}

func (owner *ContinuationGrain) OnDeactivate(context.Context, *actor.GrainProps) error {
	if owner.active != nil {
		owner.active.cancel()
	}
	return nil
}

func (owner *ContinuationGrain) OnReceive(gc *actor.GrainContext) {
	switch incoming := gc.Message().(type) {
	case continuationStart:
		if owner.closed || owner.active != nil {
			gc.Err(errors.New("continuation unavailable"))
			return
		}
		if failure := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); failure != nil {
			gc.Err(failure)
			return
		}
		owner.input = incoming
		owner.cancelled = false
		owner.upstream = gc.DeferResponse()
		owner.begin(gc, incoming.Payload)
	case continuationResult:
		// PipeToSelf can reach a new activation. Always acknowledge delivery,
		// including stale results, without reviving any previous operation.
		defer gc.NoErr()
		if incoming.Activation != owner.activation || owner.active == nil || incoming.Sequence != owner.active.sequence {
			return
		}
		owner.active.cancel()
		owner.active = nil
		if owner.cancelled {
			incoming.Failure = context.Canceled
		}
		if incoming.Failure == nil && owner.input.Next != nil {
			next := owner.input.Next
			owner.input.Next = nil
			owner.begin(gc, next)
			return
		}
		owner.finish(incoming.Payload, incoming.Failure)
	case continuationCancel:
		owner.cancelPending()
		gc.Response("cancel-accepted")
	case continuationClose:
		if owner.closed {
			gc.Response("already-closing")
			return
		}
		owner.closed = true
		if owner.active == nil {
			gc.Response("local-drained")
			return
		}
		owner.closing = gc.DeferResponse()
		owner.cancelPending()
	case requestProbe:
		gc.Response(continuationState{
			Activation:  owner.activation,
			Pending:     owner.active != nil,
			Closing:     owner.closed,
			Settlements: owner.settlements,
		})
	}
}

func (owner *ContinuationGrain) begin(gc *actor.GrainContext, payload any) {
	owner.sequence++
	sequence := owner.sequence
	activation := owner.activation
	waitContext, cancelWait := context.WithCancel(context.Background())
	outcomes := make(chan continuationResult, 1)
	exited := owner.input.Exited
	owner.active = &continuation{
		sequence: sequence,
		cancel:   cancelWait,
	}
	failure := gc.PipeToSelf(func() (any, error) {
		if exited != nil {
			defer func() { exited <- struct{}{} }()
		}
		select {
		case outcome := <-outcomes:
			return outcome, nil
		case <-waitContext.Done():
			return continuationResult{
				Activation: activation,
				Sequence:   sequence,
				Failure:    waitContext.Err(),
			}, nil
		}
	})
	if failure != nil {
		cancelWait()
		owner.active = nil
		owner.finish(nil, failure)
		return
	}
	pending := gc.RequestGrain(owner.input.Target, payload, actor.WithRequestTimeout(owner.input.Timeout))
	if pending == nil {
		// Preserve initiation failure rather than misreporting cancellation.
		outcomes <- continuationResult{
			Activation: activation,
			Sequence:   sequence,
			Failure:    errors.New("request initiation failed"),
		}
		return
	}
	owner.active.request = pending
	pending.Then(func(body any, requestFailure error) {
		outcome := continuationResult{
			Activation: activation,
			Sequence:   sequence,
			Payload:    body,
			Failure:    requestFailure,
		}
		// A late callback cannot block after task cancellation or close a
		// channel another completion is still entitled to use.
		select {
		case outcomes <- outcome:
		default:
		}
	})
}

func (owner *ContinuationGrain) cancelPending() {
	if owner.active == nil {
		return
	}
	owner.cancelled = true
	if owner.active.request != nil {
		_ = owner.active.request.Cancel()
	}
	owner.active.cancel()
}

func (owner *ContinuationGrain) finish(payload any, failure error) {
	owner.settlements++
	owner.upstream.Response(continuationOutcome{
		Payload: payload,
		Err:     failure,
	})
	owner.upstream = nil
	owner.input = continuationStart{}
	if owner.closing != nil {
		owner.closing.Response("local-drained")
		owner.closing = nil
	}
}

func makeContinuationGrain(t *testing.T, running actor.ActorSystem) *actor.GrainIdentity {
	t.Helper()
	identity, failure := actor.GrainOf[*ContinuationGrain](context.Background(), running, "continuation",
		actor.WithLongLivedGrain())
	if failure != nil {
		t.Fatal(failure)
	}
	return identity
}

func TestContinuationStartsNextRequestWithFreshContext(t *testing.T) {
	fixture := newRequestHarness(t)
	identity := makeContinuationGrain(t, fixture.system)
	downstream := makeDeferredGrain(t, fixture.system, "continuation-target")
	exited := make(chan struct{}, 2)
	sendRequestMessage(t, fixture.client, grainRequestStart{
		Target: identity,
		Payload: continuationStart{
			Target:  downstream,
			Payload: grainRun{},
			Next:    requestProbe{},
			Timeout: time.Second,
			Exited:  exited,
		},
		Timeout: 3 * time.Second,
	})
	awaitGrainStarted(t, fixture.system, downstream)
	before := inspectGrain(t, fixture.system, identity, requestProbe{}).(continuationState)
	if !before.Pending || before.Settlements != 0 {
		t.Fatalf("waiting owner not responsive: %+v", before)
	}
	inspectGrain(t, fixture.system, downstream, grainFinish{})
	outcome := readRequestOutcome(t, fixture.outcomes)
	if outcome.Err != nil {
		t.Fatal(outcome.Err)
	}
	response := outcome.Payload.(continuationOutcome)
	if response.Err != nil || !response.Payload.(grainObservation).Finished {
		t.Fatalf("second request did not complete: %+v", outcome)
	}
	awaitSignal(t, exited)
	awaitSignal(t, exited)
	// Replay of the first stage must not create a third request or reply.
	inspectGrain(t, fixture.system, identity, continuationResult{
		Activation: before.Activation,
		Sequence:   1,
	})
	after := inspectGrain(t, fixture.system, identity, requestProbe{}).(continuationState)
	if after.Pending || after.Settlements != 1 {
		t.Fatalf("duplicate stage settled twice: %+v", after)
	}
}

func TestContinuationFailureAndTimeoutExitTask(t *testing.T) {
	for _, scenario := range []string{"failure", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newRequestHarness(t)
			identity := makeContinuationGrain(t, fixture.system)
			downstream := makeDeferredGrain(t, fixture.system, "failure-target")
			exited := make(chan struct{}, 1)
			duration := time.Second
			if scenario == "timeout" {
				duration = 150 * time.Millisecond
			}
			sendRequestMessage(t, fixture.client, grainRequestStart{
				Target: identity,
				Payload: continuationStart{
					Target:  downstream,
					Payload: grainRun{},
					Next:    requestProbe{},
					Timeout: duration,
					Exited:  exited,
				},
				Timeout: 3 * time.Second,
			})
			awaitGrainStarted(t, fixture.system, downstream)
			if scenario == "failure" {
				inspectGrain(t, fixture.system, downstream, grainFinish{
					Failure: true,
				})
			}
			outcome := readRequestOutcome(t, fixture.outcomes)
			if outcome.Err != nil {
				t.Fatal(outcome.Err)
			}
			response := outcome.Payload.(continuationOutcome)
			if response.Err == nil {
				t.Fatal("lost downstream failure")
			}
			if scenario == "timeout" && !errors.Is(response.Err, gerrors.ErrRequestTimeout) {
				t.Fatalf("wrong timeout: %v", response.Err)
			}
			if scenario == "failure" && response.Err.Error() != "tool-failed" {
				t.Fatalf("wrong failure: %v", response.Err)
			}
			awaitSignal(t, exited)
			inspectGrain(t, fixture.system, downstream, grainFinish{})
			state := inspectGrain(t, fixture.system, identity, requestProbe{}).(continuationState)
			if state.Pending || state.Settlements != 1 {
				t.Fatalf("late result changed settlement: %+v", state)
			}
		})
	}
}

func TestContinuationCancelAndCloseDrainLocalWait(t *testing.T) {
	for _, closeLocal := range []bool{false, true} {
		t.Run(map[bool]string{
			false: "cancel",
			true:  "close",
		}[closeLocal], func(t *testing.T) {
			fixture := newRequestHarness(t)
			identity := makeContinuationGrain(t, fixture.system)
			downstream := makeDeferredGrain(t, fixture.system, "cancel-target")
			exited := make(chan struct{}, 1)
			sendRequestMessage(t, fixture.client, grainRequestStart{
				Target: identity,
				Payload: continuationStart{
					Target:  downstream,
					Payload: grainRun{},
					Timeout: time.Second,
					Exited:  exited,
				},
				Timeout: 3 * time.Second,
			})
			awaitGrainStarted(t, fixture.system, downstream)
			var control any = continuationCancel{}
			if closeLocal {
				control = continuationClose{}
			}
			observed := asyncGrainCall(t, fixture.system, "continuation-control", identity, control)
			if closeLocal {
				assertPayload(t, observed, "local-drained")
			} else {
				assertPayload(t, observed, "cancel-accepted")
			}
			outcome := readRequestOutcome(t, fixture.outcomes)
			if outcome.Err != nil {
				t.Fatal(outcome.Err)
			}
			response := outcome.Payload.(continuationOutcome)
			if !errors.Is(response.Err, context.Canceled) {
				t.Fatalf("cancel lost error identity: %+v", response)
			}
			awaitSignal(t, exited)
			state := inspectGrain(t, fixture.system, downstream, requestProbe{}).(grainObservation)
			if state.Finished || state.Cancelled {
				t.Fatalf("local cancellation claimed downstream cancellation: %+v", state)
			}
			inspectGrain(t, fixture.system, downstream, grainFinish{})
			final := inspectGrain(t, fixture.system, identity, requestProbe{}).(continuationState)
			if final.Pending || final.Settlements != 1 || final.Closing != closeLocal {
				t.Fatalf("local wait not drained: %+v", final)
			}
		})
	}
}

func TestContinuationStopExitsTaskAndRejectsPreviousActivation(t *testing.T) {
	fixture := newRequestHarness(t)
	identity := makeContinuationGrain(t, fixture.system)
	downstream := makeDeferredGrain(t, fixture.system, "stop-target")
	exited := make(chan struct{}, 1)
	sendRequestMessage(t, fixture.client, grainRequestStart{
		Target: identity,
		Payload: continuationStart{
			Target:  downstream,
			Payload: grainRun{},
			Timeout: time.Second,
			Exited:  exited,
		},
		Timeout: 150 * time.Millisecond,
	})
	awaitGrainStarted(t, fixture.system, downstream)
	before := inspectGrain(t, fixture.system, identity, requestProbe{}).(continuationState)
	if failure := fixture.system.TellGrain(context.Background(), identity, new(actor.PoisonPill)); failure != nil {
		t.Fatal(failure)
	}
	awaitSignal(t, exited)
	// A successful Tell stop completes deactivation, but identity remains
	// activatable. An old completion is not an authorization to resume work.
	inspectGrain(t, fixture.system, identity, continuationResult{
		Activation: before.Activation,
		Sequence:   1,
		Payload:    "old-result",
	})
	after := inspectGrain(t, fixture.system, identity, requestProbe{}).(continuationState)
	if after.Activation == before.Activation || after.Pending || after.Settlements != 0 {
		t.Fatalf("old completion revived operation: %+v", after)
	}
	inspectGrain(t, fixture.system, downstream, grainFinish{})
	outcome := readRequestOutcome(t, fixture.outcomes)
	if outcome.Err == nil {
		t.Fatal("forced stop reported business success")
	}

	// Start a new operation whose local sequence is also 1. An idle-only
	// stale-result test would not prove the activation guard is necessary.
	freshTarget := makeDeferredGrain(t, fixture.system, "reactivated-target")
	freshExit := make(chan struct{}, 1)
	sendRequestMessage(t, fixture.client, grainRequestStart{
		Target: identity,
		Payload: continuationStart{
			Target:  freshTarget,
			Payload: grainRun{},
			Timeout: time.Second,
			Exited:  freshExit,
		},
		Timeout: 3 * time.Second,
	})
	awaitGrainStarted(t, fixture.system, freshTarget)
	inspectGrain(t, fixture.system, identity, continuationResult{
		Activation: before.Activation,
		Sequence:   1,
		Payload:    "old-result",
	})
	current := inspectGrain(t, fixture.system, identity, requestProbe{}).(continuationState)
	if !current.Pending || current.Settlements != 0 {
		t.Fatalf("old completion settled new operation: %+v", current)
	}
	inspectGrain(t, fixture.system, freshTarget, grainFinish{})
	freshOutcome := readRequestOutcome(t, fixture.outcomes)
	if freshOutcome.Err != nil {
		t.Fatal(freshOutcome.Err)
	}
	response := freshOutcome.Payload.(continuationOutcome)
	if response.Err != nil || response.Payload != "body" {
		t.Fatalf("reactivated request failed: %+v", response)
	}
	awaitSignal(t, freshExit)
}
