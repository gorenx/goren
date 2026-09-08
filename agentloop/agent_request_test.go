package agentloop_test

import (
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/agentloop/internal/modelrequest"
)

func TestModelRequestUsesAgentCancellationAtEachBoundary(t *testing.T) {
	for _, boundary := range []string{"before request", "history", "configuration"} {
		t.Run(boundary, func(t *testing.T) {
			subject, history := requestingStep(t)
			var completion modelrequest.RequestResult = modelrequest.RequestRead{
				Snapshot: history.Snapshot(),
			}
			if boundary != "before request" {
				if _, err := subject.StartRequest(); err != nil {
					t.Fatal(err)
				}
				if _, err := subject.StartRequest(); err == nil {
					t.Fatal("second request accepted")
				}
				if _, err := subject.BeginModel(agentloop.ModelInput{}); err == nil {
					t.Fatal("separate attempt accepted while request owned execution")
				}
			}
			if boundary == "configuration" {
				next, err := subject.AdvanceRequest(completion)
				if _, ok := next.(modelrequest.ResolveConfiguration); err != nil || !ok {
					t.Fatalf("configuration boundary: %T, %v", next, err)
				}
				completion = modelrequest.ConfigurationResolved{}
			}
			if _, err := subject.Cancel(agent.ParentCancel{}, agent.CancelOptions{
				KeepInbox: true,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := subject.BeginModel(agentloop.ModelInput{}); err == nil {
				t.Fatal("canceled Agent started attempt")
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
