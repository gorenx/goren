package toolactor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/tools/tool/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type bind struct {
	target  *goakt.GrainIdentity
	version uint64
}

type caller struct {
	endpoint     *toolactor.Client
	execution    *goakt.GrainReply
	cancellation *goakt.GrainReply
}

func (*caller) OnActivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *caller) OnDeactivate(context.Context, *goakt.GrainProps) error {
	if owner.endpoint != nil {
		owner.endpoint.Dispose()
	}
	return nil
}

func (owner *caller) OnReceive(gc *goakt.GrainContext) {
	if owner.endpoint != nil && owner.endpoint.Receive(gc, toolactor.Receivers{
		Execution:    owner,
		Cancellation: owner,
	}) {
		return
	}
	switch input := gc.Message().(type) {
	case bind:
		if owner.endpoint != nil {
			owner.endpoint.Dispose()
		}
		endpoint, err := toolactor.NewClient(input.target, "creation", input.version)
		owner.endpoint = endpoint
		gc.Response(toolactor.Completion{
			Err: err,
		})
	case toolactor.Execute:
		owner.execution = gc.DeferResponse()
		if err := owner.endpoint.Execute(gc, input.Context, input.Invocation); err != nil {
			owner.execution.Err(err)
		}
	case toolactor.CancelCall:
		owner.cancellation = gc.DeferResponse()
		if err := owner.endpoint.Cancel(gc, input.CallID); err != nil {
			owner.cancellation.Err(err)
		}
	default:
		gc.Unhandled()
	}
}

func (owner *caller) OnToolExecuted(_ *goakt.GrainContext, result toolactor.CallResult) {
	owner.execution.Response(result)
	owner.execution = nil
}

func (owner *caller) OnToolCancellation(_ *goakt.GrainContext, result toolactor.Cancellation) {
	owner.cancellation.Response(result)
	owner.cancellation = nil
}

func startCaller(t *testing.T, system goakt.ActorSystem, target *goakt.GrainIdentity, version uint64) *goakt.GrainIdentity {
	t.Helper()
	identity, err := goakt.GrainOf[*caller](context.Background(), system, "caller",
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	if reply := ask[toolactor.Completion](t, system, identity, bind{
		target:  target,
		version: version,
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	return identity
}

func TestClientReceivesActualBodyResultAndBusinessFailure(t *testing.T) {
	system, target := installTool(t, &provider{})
	consumer := startCaller(t, system, target, 1)
	result := ask[toolactor.CallResult](t, system, consumer, invocation())
	if result.CallID != "call" || result.Err != nil || result.Body.Err != nil || len(result.Body.Value) == 0 {
		t.Fatalf("body result: %#v", result)
	}
	// A version rejection is a confirmed body-layer failure, not a lost reply.
	if reply := ask[toolactor.Completion](t, system, consumer, bind{
		target:  target,
		version: 2,
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	result = ask[toolactor.CallResult](t, system, consumer, invocation())
	if result.Err != nil || result.Body.Err == nil {
		t.Fatalf("business failure was confused with delivery failure: %#v", result)
	}
	if reply := ask[toolactor.Completion](t, system, target, toolactor.Close{
		CreationID: "creation",
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
}

type invalidTool struct{}

func (*invalidTool) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*invalidTool) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (*invalidTool) OnReceive(gc *goakt.GrainContext)                      { gc.Response("not a body result") }

func TestClientDoesNotTreatInvalidReplyAsCompletedBody(t *testing.T) {
	system, _, _ := startTool(t)
	target, err := goakt.GrainOf[*invalidTool](context.Background(), system, "invalid-tool", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	consumer := startCaller(t, system, target, 1)
	result := ask[toolactor.CallResult](t, system, consumer, invocation())
	if result.Err == nil || result.Body.Err != nil || result.CallID != "call" {
		t.Fatalf("invalid reply was accepted as business outcome: %#v", result)
	}
}

func TestClientCancellationDoesNotCompleteTheBodyRequest(t *testing.T) {
	service := &waitingQuestions{
		entered:   make(chan struct{}, 1),
		cancelled: make(chan struct{}, 1),
		release:   make(chan struct{}),
	}
	system, target := installTool(t, &provider{
		questions: service,
	})
	consumer := startCaller(t, system, target, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		select {
		case <-service.release:
		default:
			close(service.release)
		}
	})
	results := make(chan toolactor.CallResult, 1)
	go func() {
		input := invocation()
		input.Context = ctx
		payload, err := system.AskGrain(context.Background(), consumer, input, 3*time.Second)
		result, ok := payload.(toolactor.CallResult)
		if !ok && err == nil {
			err = errors.New("invalid client result")
		}
		if err != nil {
			result.Err = err
		}
		results <- result
	}()
	select {
	case <-service.entered:
	case <-time.After(time.Second):
		t.Fatal("body did not start")
	}
	if result := ask[toolactor.Cancellation](t, system, consumer, toolactor.CancelCall{
		CallID: "call",
	}); result.Err != nil || result.CallID != "call" {
		t.Fatalf("cancel result: %#v", result)
	}
	select {
	case <-results:
		t.Fatal("cancellation completed the original execution")
	default:
	}
	close(service.release)
	select {
	case result := <-results:
		if result.Err != nil || !errors.Is(result.Body.Err, context.Canceled) {
			t.Fatalf("body cancellation result: %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("body completion did not reach the client receiver")
	}
	if result := ask[toolactor.Completion](t, system, target, toolactor.Close{
		CreationID: "creation",
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
}
