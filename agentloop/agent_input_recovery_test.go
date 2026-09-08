package agentloop_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/session"
)

func TestInputRecoveryRetainsUncertaintyAndAppliesClaimOnce(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "not committed"
		if committed {
			name = "reply lost"
		}
		t.Run(name, func(t *testing.T) {
			subject, history := openTurn(t)
			edit, err := subject.PrepareInput()
			if err != nil {
				t.Fatal(err)
			}
			if committed {
				if _, err := history.Commit(context.Background(), session.Batch(edit.Drafts...)); err != nil {
					t.Fatal(err)
				}
			}
			invalid := history.Snapshot()
			invalid.Barrier.SessionID = "foreign"
			if _, err := subject.ReconcileInput(invalid); err == nil {
				t.Fatal("foreign recovery accepted")
			}
			if _, err := subject.Inject(inputMessage(t, "conflicting edit")); !errors.Is(err, agentloop.ErrState) {
				t.Fatal("unconfirmed claim admitted another mutation", err)
			}
			if _, err := subject.Cancel(agent.UserCancel{}, agent.CancelOptions{}); !errors.Is(err, agentloop.ErrState) {
				t.Fatal("uncertain Inbox falsely cleared", err)
			}
			if subject.Cancellation() == nil {
				t.Fatal("cancel intent lost when clear was rejected")
			}
			found, err := subject.ReconcileInput(history.Snapshot())
			if err != nil || found != committed {
				t.Fatalf("recovery = %v, %v", found, err)
			}
			if subject.InboxValue().HasPending() == committed {
				t.Fatal("recovery projected wrong claim outcome")
			}
			if _, err := subject.ReconcileInput(history.Snapshot()); !errors.Is(err, agentloop.ErrState) {
				t.Fatal("claim recovered twice", err)
			}
			if committed {
				if err := subject.AbandonPreparation(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := subject.PrepareTurnEnd(subject.Cancellation()); err != nil {
				t.Fatal("resolved claim blocked Turn settlement", err)
			}
		})
	}
}
