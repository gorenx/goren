package spike

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tochemey/goakt/v4/actor"
	gerrors "github.com/tochemey/goakt/v4/errors"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type requestStart struct {
	Target  *actor.PID
	Payload any
	Timeout time.Duration
}
type requestCancel struct{}
type requestProbe struct{}
type requestOutcome struct {
	Payload any
	Err     error
}
type requestWork struct{}
type requestFinish struct{}
type requestBusinessCancel struct{}
type requestImmediate struct{}
type requestSnapshot struct {
	Started     bool
	Finished    bool
	Cancelled   bool
	Correlation string
}

// requestClient registers continuations in Receive. Only the test driver reads
// its buffered observations; no actor waits on those channels.
type requestClient struct {
	outcomes  chan requestOutcome
	handle    actor.RequestCall
	callbacks int
}

func (*requestClient) PreStart(*actor.Context) error { return nil }
func (*requestClient) PostStop(*actor.Context) error { return nil }
func (owner *requestClient) Receive(rc *actor.ReceiveContext) {
	switch incoming := rc.Message().(type) {
	case requestStart:
		owner.handle = rc.Request(incoming.Target, incoming.Payload, actor.WithRequestTimeout(incoming.Timeout))
		if owner.handle == nil {
			owner.outcomes <- requestOutcome{Err: errors.New("request initiation failed")}
			return
		}
		owner.handle.Then(func(payload any, failure error) {
			owner.callbacks++
			owner.outcomes <- requestOutcome{
				Payload: payload,
				Err:     failure,
			}
		})
	case grainRequestStart:
		owner.handle = rc.RequestGrain(incoming.Target, incoming.Payload, actor.WithRequestTimeout(incoming.Timeout))
		if owner.handle == nil {
			owner.outcomes <- requestOutcome{Err: errors.New("grain request initiation failed")}
			return
		}
		owner.handle.Then(func(payload any, failure error) {
			owner.callbacks++
			owner.outcomes <- requestOutcome{
				Payload: payload,
				Err:     failure,
			}
		})
	case requestCancel:
		var failure error
		if owner.handle != nil {
			failure = owner.handle.Cancel()
		}
		rc.Response(requestOutcome{Err: failure})
	case requestProbe:
		rc.Response(owner.callbacks)
	}
}

// requestTarget deliberately returns from Receive before finishing work. It
// never retains ReceiveContext. Response during requestFinish cannot reply to
// the earlier Request: the completion message has no request metadata.
// A downstream PID makes this a Runtime-shaped relay for the chain probe.
type requestTarget struct {
	downstream *actor.PID
	state      requestSnapshot
}

func (*requestTarget) PreStart(*actor.Context) error { return nil }
func (*requestTarget) PostStop(*actor.Context) error { return nil }
func (owner *requestTarget) Receive(rc *actor.ReceiveContext) {
	switch rc.Message().(type) {
	case requestImmediate:
		rc.Response("body-finished")
	case requestWork:
		owner.state.Started = true
		owner.state.Correlation = rc.CorrelationID()
		if owner.downstream != nil {
			self := rc.Self()
			pending := rc.Request(owner.downstream, requestImmediate{}, actor.WithRequestTimeout(time.Second))
			if pending == nil {
				return
			}
			pending.Then(func(_ any, failure error) {
				if failure == nil {
					// Continue with a fresh message, without capturing the old ReceiveContext.
					_ = actor.Tell(context.Background(), self, requestFinish{})
				}
			})
		}
	case requestFinish:
		owner.state.Finished = true
		rc.Response("body-finished")
	case requestBusinessCancel:
		owner.state.Cancelled = true
		rc.Response("cancel-accepted")
	case requestProbe:
		rc.Response(owner.state)
	}
}

type requestHarness struct {
	system   actor.ActorSystem
	client   *actor.PID
	target   *actor.PID
	outcomes <-chan requestOutcome
}

func newRequestHarness(t *testing.T) requestHarness {
	t.Helper()
	running, failure := actor.NewActorSystem("request-spike", actor.WithLogger(log.DiscardLogger))
	if failure != nil {
		t.Fatal(failure)
	}
	if failure = running.Start(context.Background()); failure != nil {
		t.Fatal(failure)
	}
	t.Cleanup(func() {
		if failure := running.Stop(context.Background()); failure != nil {
			t.Error(failure)
		}
	})
	outcomes := make(chan requestOutcome, 8)
	clientPID := spawnRequestActor(t, running, "requester", &requestClient{outcomes: outcomes})
	targetPID := spawnRequestActor(t, running, "target", &requestTarget{})
	return requestHarness{
		system:   running,
		client:   clientPID,
		target:   targetPID,
		outcomes: outcomes,
	}
}
func spawnRequestActor(t *testing.T, running actor.ActorSystem, name string, instance actor.Actor) *actor.PID {
	t.Helper()
	spawned, failure := running.Spawn(context.Background(), name, instance,
		actor.WithLongLived(),
		actor.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if failure != nil {
		t.Fatal(failure)
	}
	return spawned
}
func sendRequestMessage(t *testing.T, targetPID *actor.PID, payload any) {
	t.Helper()
	if failure := actor.Tell(context.Background(), targetPID, payload); failure != nil {
		t.Fatal(failure)
	}
}
func readRequestOutcome(t *testing.T, feed <-chan requestOutcome) requestOutcome {
	t.Helper()
	select {
	case observed := <-feed:
		return observed
	case <-time.After(3 * time.Second):
		t.Fatal("request callback missing")
	}
	return requestOutcome{}
}
func awaitRequestTarget(t *testing.T, targetPID *actor.PID, finished bool) requestSnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		observed := ask(t, targetPID, requestProbe{}).(requestSnapshot)
		if observed.Started && observed.Finished == finished {
			return observed
		}
		if time.Now().After(deadline) {
			t.Fatalf("target did not reach requested state: %+v", observed)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRequestImmediateResponseAndContinuation(t *testing.T) {
	fixture := newRequestHarness(t)
	sendRequestMessage(t, fixture.client, requestStart{
		Target:  fixture.target,
		Payload: requestImmediate{},
		Timeout: time.Second,
	})
	observed := readRequestOutcome(t, fixture.outcomes)
	if observed.Err != nil || observed.Payload != "body-finished" {
		t.Fatalf("unexpected response: %+v", observed)
	}
	if count := ask(t, fixture.client, requestProbe{}); count != 1 {
		t.Fatalf("callback count: %v", count)
	}
}

func TestRequestCancelKeepsBothMailboxesResponsiveAndDoesNotCancelWork(t *testing.T) {
	fixture := newRequestHarness(t)
	sendRequestMessage(t, fixture.client, requestStart{
		Target:  fixture.target,
		Payload: requestWork{},
		Timeout: 2 * time.Second,
	})
	observed := awaitRequestTarget(t, fixture.target, false)
	if observed.Correlation == "" {
		t.Fatal("missing Request correlation")
	}
	if count := ask(t, fixture.client, requestProbe{}); count != 0 {
		t.Fatalf("request completed early: %v", count)
	}
	if outcome := ask(t, fixture.client, requestCancel{}).(requestOutcome); outcome.Err != nil {
		t.Fatal(outcome.Err)
	}
	cancelled := readRequestOutcome(t, fixture.outcomes)
	if !errors.Is(cancelled.Err, gerrors.ErrRequestCanceled) {
		t.Fatalf("unexpected cancellation: %+v", cancelled)
	}
	observed = ask(t, fixture.target, requestProbe{}).(requestSnapshot)
	if observed.Cancelled || observed.Finished {
		t.Fatalf("request cancellation changed operation: %+v", observed)
	}
	// Cancellation of the actual operation is a distinct asynchronous Request.
	sendRequestMessage(t, fixture.client, requestStart{
		Target:  fixture.target,
		Payload: requestBusinessCancel{},
		Timeout: time.Second,
	})
	if outcome := readRequestOutcome(t, fixture.outcomes); outcome.Err != nil || outcome.Payload != "cancel-accepted" {
		t.Fatalf("business cancel: %+v", outcome)
	}
	observed = ask(t, fixture.target, requestProbe{}).(requestSnapshot)
	if !observed.Cancelled || observed.Finished {
		t.Fatalf("cancel acknowledgement confused with completion: %+v", observed)
	}
}

func TestRequestTimeoutCanQueryFinalStateWithoutReexecuting(t *testing.T) {
	fixture := newRequestHarness(t)
	sendRequestMessage(t, fixture.client, requestStart{
		Target:  fixture.target,
		Payload: requestWork{},
		Timeout: 100 * time.Millisecond,
	})
	awaitRequestTarget(t, fixture.target, false)
	if outcome := readRequestOutcome(t, fixture.outcomes); !errors.Is(outcome.Err, gerrors.ErrRequestTimeout) {
		t.Fatalf("wanted timeout: %+v", outcome)
	}
	sendRequestMessage(t, fixture.target, requestFinish{})
	awaitRequestTarget(t, fixture.target, true)
	sendRequestMessage(t, fixture.client, requestStart{
		Target:  fixture.target,
		Payload: requestProbe{},
		Timeout: time.Second,
	})
	outcome := readRequestOutcome(t, fixture.outcomes)
	if outcome.Err != nil {
		t.Fatal(outcome.Err)
	}
	if observed := outcome.Payload.(requestSnapshot); !observed.Finished {
		t.Fatalf("final state lost: %+v", observed)
	}
	if count := ask(t, fixture.client, requestProbe{}); count != 2 {
		t.Fatalf("old completion invoked callback again: %v", count)
	}
}

// This is a characterization of a framework limitation, NOT a passing
// end-to-end acceptance: a new Receive's Response does not finish an old Request.
func TestRequestCompletionInNewReceiveDoesNotReplyToOriginalCall(t *testing.T) {
	fixture := newRequestHarness(t)
	sendRequestMessage(t, fixture.client, requestStart{
		Target:  fixture.target,
		Payload: requestWork{},
		Timeout: time.Second,
	})
	awaitRequestTarget(t, fixture.target, false)
	sendRequestMessage(t, fixture.target, requestFinish{})
	awaitRequestTarget(t, fixture.target, true)
	if count := ask(t, fixture.client, requestProbe{}); count != 0 {
		t.Fatalf("request ended before task completion was inspected: %v", count)
	}
	if outcome := readRequestOutcome(t, fixture.outcomes); !errors.Is(outcome.Err, gerrors.ErrRequestTimeout) {
		t.Fatalf("expected missing deferred reply: %+v", outcome)
	}
}

func TestRequestNestedContinuationCannotReplyUsingFreshReceive(t *testing.T) {
	fixture := newRequestHarness(t)
	relayPID := spawnRequestActor(t, fixture.system, "runtime", &requestTarget{downstream: fixture.target})
	sendRequestMessage(t, fixture.client, requestStart{
		Target:  relayPID,
		Payload: requestWork{},
		Timeout: time.Second,
	})
	awaitRequestTarget(t, relayPID, true)
	if count := ask(t, fixture.client, requestProbe{}); count != 0 {
		t.Fatalf("request ended before downstream completion was inspected: %v", count)
	}
	if outcome := readRequestOutcome(t, fixture.outcomes); !errors.Is(outcome.Err, gerrors.ErrRequestTimeout) {
		t.Fatalf("expected upstream reply gap: %+v", outcome)
	}
}
