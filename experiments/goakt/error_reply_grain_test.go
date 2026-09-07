package spike

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tochemey/goakt/v4/actor"
)

type errorReply struct {
	Err       error
	Framework bool
}

type operationFailure struct {
	operation string
	cause     error
}

func (detail *operationFailure) Error() string {
	return detail.operation + ": " + detail.cause.Error()
}

func (detail *operationFailure) Unwrap() error {
	return detail.cause
}

// ErrorReplyGrain contrasts local business error values with framework errors.
type ErrorReplyGrain struct{}

func (*ErrorReplyGrain) OnActivate(context.Context, *actor.GrainProps) error {
	return nil
}

func (*ErrorReplyGrain) OnDeactivate(context.Context, *actor.GrainProps) error {
	return nil
}

func (*ErrorReplyGrain) OnReceive(gc *actor.GrainContext) {
	incoming := gc.Message().(errorReply)
	if incoming.Framework {
		gc.Err(incoming.Err)
		return
	}
	gc.Response(continuationOutcome{
		Err: incoming.Err,
	})
}

func TestLocalBusinessErrorRetainsIdentityAndWrapping(t *testing.T) {
	fixture := newRequestHarness(t)
	identity, failure := actor.GrainOf[*ErrorReplyGrain](context.Background(), fixture.system, "error-reply")
	if failure != nil {
		t.Fatal(failure)
	}
	detail := &operationFailure{
		operation: "read",
		cause:     context.Canceled,
	}
	wrapped := fmt.Errorf("tool execution: %w", detail)
	outcomes := asyncGrainCall(t, fixture.system, "business-error", identity, errorReply{
		Err: wrapped,
	})
	outcome := readRequestOutcome(t, outcomes)
	if outcome.Err != nil {
		t.Fatal(outcome.Err)
	}
	response := outcome.Payload.(continuationOutcome)
	var decoded *operationFailure
	if response.Err != wrapped || !errors.Is(response.Err, context.Canceled) || !errors.As(response.Err, &decoded) || decoded != detail {
		t.Fatalf("local response lost error identity or wrapping: %v", response.Err)
	}

	// The same error through the framework error envelope loses its Go type.
	failures := asyncGrainCall(t, fixture.system, "framework-error", identity, errorReply{
		Err:       wrapped,
		Framework: true,
	})
	framework := readRequestOutcome(t, failures)
	decoded = nil
	if framework.Err == nil || framework.Err.Error() != wrapped.Error() {
		t.Fatalf("framework did not retain the description: %v", framework.Err)
	}
	if errors.Is(framework.Err, context.Canceled) || errors.As(framework.Err, &decoded) {
		t.Fatal("framework limitation changed; revisit the error contract")
	}
}
