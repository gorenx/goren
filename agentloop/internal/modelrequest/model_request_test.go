package modelrequest

import (
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

func TestModelRequestStartsAttemptWithoutConfiguration(t *testing.T) {
	current := New()
	next, err := current.Start(RequestInput{
		SessionID: "conversation",
		Position: session.StepPosition{
			Turn: 1,
			Step: 1,
		},
		First: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	running, ok := next.(RunAttempt)
	if !ok {
		t.Fatalf("first operation = %T", next)
	}
	if _, ok := running.Operation.(ReadLog); !ok {
		t.Fatalf("input operation = %T", running.Operation)
	}
	if _, err := current.Outcome(); err == nil {
		t.Fatal("unfinished request exposed outcome")
	}
	if _, err := current.Advance(RecoveryResolved{}, agent.NewCancellation(nil)); err == nil {
		t.Fatal("accepted mismatched result")
	}
	if current.Attempts() != 1 {
		t.Fatalf("attempts = %d", current.Attempts())
	}
}

func TestModelRequestFailureCannotRestartGeneration(t *testing.T) {
	current := New()
	input := RequestInput{
		SessionID: "conversation",
		Position: session.StepPosition{
			Turn: 1,
			Step: 1,
		},
		First: true,
	}
	if _, err := current.Start(input); err != nil {
		t.Fatal(err)
	}
	problem := errors.New("session unavailable")
	next, err := current.Advance(RequestFailed{
		Err: problem,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	running, ok := next.(RunAttempt)
	if !ok {
		t.Fatalf("cleanup operation = %T", next)
	}
	if _, ok := running.Operation.(CloseStream); !ok {
		t.Fatalf("cleanup = %T", running.Operation)
	}
	next, err = current.Advance(AttemptAdvanced{
		Result: StreamClosed{},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	completed, ok := next.(RequestFinished)
	if !ok || !errors.Is(completed.Result.Err, problem) || !completed.Result.StreamClosed {
		t.Fatalf("failure outcome = %#v", next)
	}
	if _, err := current.Start(input); err == nil {
		t.Fatal("terminal request restarted")
	}
	if _, err := current.Advance(RecoveryResolved{
		Action: agent.RequestErrorAction{
			Retry: true,
		},
	}, nil); err == nil {
		t.Fatal("ordinary failure accepted recovery")
	}
	next, err = current.RetryClose()
	if err != nil {
		t.Fatal(err)
	}
	retained, ok := next.(RequestFinished)
	if !ok || !errors.Is(retained.Result.Err, problem) {
		t.Fatalf("cleanup lost original error: %#v", next)
	}
	if current.Attempts() != 1 {
		t.Fatal("failed read restarted the attempt")
	}
}
