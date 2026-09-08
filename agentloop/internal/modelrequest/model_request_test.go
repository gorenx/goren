package modelrequest

import (
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func TestModelRequestRequiresConfigurationBeforeAttempt(t *testing.T) {
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
	if _, ok := next.(ReadRequest); !ok {
		t.Fatalf("first operation = %T", next)
	}
	if _, err := current.Outcome(); err == nil {
		t.Fatal("unfinished request exposed an outcome")
	}
	// A mismatched completion must neither start an attempt nor record cancellation.
	if _, err := current.Advance(CallPrepared{}, agent.NewCancellation(nil)); err == nil {
		t.Fatal("accepted preparation before configuration")
	}
	next, err = current.Advance(RequestRead{
		Snapshot: session.Snapshot{
			Barrier: session.WriteBarrier{
				SessionID: "conversation",
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := next.(ResolveConfiguration); !ok {
		t.Fatalf("configuration operation = %T", next)
	}
	if current.Attempts() != 0 {
		t.Fatal("attempt counted before preparation")
	}
	config := llm.CallConfig{
		Provider: "provider",
		Model:    "model",
	}
	next, err = current.Advance(ConfigurationResolved{
		Config: config,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := next.(PrepareCall); !ok {
		t.Fatalf("prepare operation = %T", next)
	}
	next, err = current.Advance(CallPrepared{
		Config: config,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	running, ok := next.(RunAttempt)
	if !ok {
		t.Fatalf("attempt operation = %T", next)
	}
	if _, ok := running.Operation.(ReadLog); !ok {
		t.Fatalf("attempt input operation = %T", running.Operation)
	}
	if current.Attempts() != 1 {
		t.Fatalf("attempts = %d, want 1", current.Attempts())
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
	if current.Attempts() != 0 {
		t.Fatal("failed baseline read started generation")
	}
}
