package agentloop_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/session"
)

type startPlan struct {
	fact agentloop.TurnStart
}

func (plan startPlan) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	return plan.fact.Drafts(snapshot)
}
func (plan startPlan) FindCommit(snapshot session.Snapshot) (*session.WriteResult, error) {
	return plan.fact.Find(snapshot)
}

func TestTurnStartRetriesSameFactAfterCancellation(t *testing.T) {
	for _, alreadyCommitted := range []bool{false, true} {
		t.Run(map[bool]string{false: "not committed", true: "reply lost"}[alreadyCommitted], func(t *testing.T) {
			subject, history := readyAgent(t)
			input, err := subject.Followup(inputMessage(t, "turn"))
			if err != nil {
				t.Fatal(err)
			}
			confirmInput(t, subject, history, input)
			beginning, err := subject.PrepareTurn()
			if err != nil {
				t.Fatal(err)
			}
			var original session.WriteResult
			if alreadyCommitted {
				original, err = history.Commit(context.Background(), startPlan{
					fact: beginning,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := subject.Cancel(agent.ParentCancel{}, agent.CancelOptions{
				KeepInbox: true,
			}); err != nil {
				t.Fatal(err)
			}
			retry, err := subject.PrepareTurn()
			if err != nil {
				t.Fatal(err)
			}
			write, err := history.Commit(context.Background(), startPlan{
				fact: retry,
			})
			if err != nil {
				t.Fatal(err)
			}
			if alreadyCommitted && !reflect.DeepEqual(write, original) {
				t.Fatal("retry changed original commit")
			}
			if err := subject.ConfirmTurn(write); err != nil {
				t.Fatal(err)
			}
			if _, err := subject.PrepareTurn(); err == nil {
				t.Fatal("confirmed Turn restarted")
			}
			next, err := subject.ContinueTurn(nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := turnReason(t, history, next).(session.TurnAborted); !ok {
				t.Fatal("lost cancellation")
			}
			// Session can still acknowledge the original start after that Turn ends.
			repeated, err := history.Commit(context.Background(), startPlan{
				fact: beginning,
			})
			if err != nil || !reflect.DeepEqual(repeated, write) {
				t.Fatalf("later retry: %+v, %v", repeated, err)
			}
		})
	}
}
