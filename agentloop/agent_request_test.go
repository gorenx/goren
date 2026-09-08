package agentloop_test

import (
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop/internal/modelrequest"
)

func TestModelRequestUsesAgentCancellationAtEachBoundary(t *testing.T) {
	for _, boundary := range []string{"before request", "history", "generation"} {
		t.Run(boundary, func(t *testing.T) {
			subject, history := requestingStep(t)
			var completion modelrequest.RequestResult = modelrequest.AttemptAdvanced{
				Result: modelrequest.LogRead{
					Snapshot: history.Snapshot(),
				},
			}
			if boundary != "before request" {
				if _, err := subject.StartRequest(); err != nil {
					t.Fatal(err)
				}
				if _, err := subject.StartRequest(); err == nil {
					t.Fatal("second request accepted")
				}
			}
			if boundary == "generation" {
				next, err := subject.AdvanceRequest(completion)
				if _, ok := next.(modelrequest.RunAttempt); err != nil || !ok {
					t.Fatalf("configuration boundary: %T, %v", next, err)
				}
				completion = modelrequest.AttemptAdvanced{
					Result: modelrequest.GenerationStarted{},
				}
			}
			if _, err := subject.Cancel(agent.ParentCancel{}, agent.CancelOptions{
				KeepInbox: true,
			}); err != nil {
				t.Fatal(err)
			}
			if boundary == "before request" {
				_, err := subject.StartRequest()
				var intent agent.Cancellation
				if !errors.As(err, &intent) {
					t.Fatalf("request ignored cancellation: %v", err)
				}
				return
			}
			next, err := subject.AdvanceRequest(completion)
			if err != nil {
				t.Fatal(err)
			}
			running, ok := next.(modelrequest.RunAttempt)
			if !ok {
				t.Fatalf("cleanup operation = %T", next)
			}
			if _, ok := running.Operation.(modelrequest.CloseStream); !ok {
				t.Fatalf("cleanup = %T", running.Operation)
			}
			next, err = subject.AdvanceRequest(modelrequest.AttemptAdvanced{
				Result: modelrequest.StreamClosed{},
			})
			if err != nil {
				t.Fatal(err)
			}
			finished, ok := next.(modelrequest.RequestFinished)
			if !ok {
				t.Fatalf("canceled request advanced: %T", next)
			}
			var intent agent.Cancellation
			if !errors.As(finished.Result.Err, &intent) || intent.Cause.CancelKind() != "parent" || !finished.Result.StreamClosed {
				t.Fatalf("request outcome: %+v", finished.Result)
			}
			if _, err := subject.CompleteModel(); err != nil {
				t.Fatal(err)
			}
			if _, err := subject.CompleteModel(); err == nil {
				t.Fatal("request consumed twice")
			}
		})
	}
}
