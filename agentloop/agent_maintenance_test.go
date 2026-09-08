package agentloop_test

import (
	"errors"
	"github.com/gorenx/goren/agent"
	"testing"
)

func TestMaintenanceOwnsActivityUntilSettlement(t *testing.T) {
	subject, history := readyAgent(t)
	if err := subject.BeginMaintenance(); err != nil {
		t.Fatal(err)
	}
	if subject.StatusValue() != agent.StatusRunning {
		t.Fatal("maintenance must report running")
	}
	if err := subject.BeginMaintenance(); !errors.Is(err, agent.ErrBusy) {
		t.Fatalf("overlapping maintenance: %v", err)
	}
	if _, err := subject.PrepareTurn(); err == nil {
		t.Fatal("Turn started during maintenance")
	}
	if _, err := subject.FinishActivity(); err == nil {
		t.Fatal("execution completion released maintenance")
	}
	proposal, err := subject.Followup(inputMessage(t, "after maintenance"))
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	replay, err := subject.FinishMaintenance()
	if err != nil || !replay {
		t.Fatalf("replay: %v, %v", replay, err)
	}
	if _, err := subject.FinishMaintenance(); err == nil {
		t.Fatal("duplicate maintenance completion accepted")
	}
	if _, err := subject.PrepareTurn(); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceCancellationDoesNotReleaseActivity(t *testing.T) {
	subject, history := readyAgent(t)
	if err := subject.BeginMaintenance(); err != nil {
		t.Fatal(err)
	}
	if _, err := subject.Cancel(agent.ParentCancel{}, agent.CancelOptions{
		KeepInbox: true,
	}); err != nil {
		t.Fatal(err)
	}
	if subject.Cancellation() == nil || subject.StatusValue() != agent.StatusRunning {
		t.Fatal("cancel released maintenance")
	}
	proposal, err := subject.Steer(inputMessage(t, "after cancel"))
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	if len(subject.InboxValue().NextTurn()) != 1 {
		t.Fatal("cancelled input must target next Turn")
	}
	replay, err := subject.FinishMaintenance()
	if err != nil || !replay || subject.Cancellation() != nil {
		t.Fatalf("replay: %v, %v", replay, err)
	}
}

func TestMaintenanceDoesNotWakeInjectedInput(t *testing.T) {
	subject, history := readyAgent(t)
	if err := subject.BeginMaintenance(); err != nil {
		t.Fatal(err)
	}
	proposal, err := subject.Inject(inputMessage(t, "injected"))
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	replay, err := subject.FinishMaintenance()
	if err != nil || replay || subject.StatusValue() != agent.StatusIdle {
		t.Fatalf("unexpected replay: %v, %v", replay, err)
	}
	if !subject.InboxValue().HasPending() {
		t.Fatal("injected input lost")
	}
}

func TestMaintenanceSuppressesReplayAfterRemovalOrDisposal(t *testing.T) {
	for _, disposed := range []bool{false, true} {
		t.Run(map[bool]string{false: "removed", true: "disposed"}[disposed], func(t *testing.T) {
			subject, history := readyAgent(t)
			if err := subject.BeginMaintenance(); err != nil {
				t.Fatal(err)
			}
			if disposed {
				if _, err := subject.Cancel(agent.DisposedCancel{}, agent.CancelOptions{
					KeepInbox: true,
				}); err != nil {
					t.Fatal(err)
				}
			}
			input := inputMessage(t, "pending")
			proposal, err := subject.Followup(input)
			if err != nil {
				t.Fatal(err)
			}
			confirmInput(t, subject, history, proposal)
			if !disposed {
				edit, err := subject.Remove(input.StableID())
				if err != nil {
					t.Fatal(err)
				}
				confirmInput(t, subject, history, agent.Admission{
					Edit: edit,
				})
			}
			replay, err := subject.FinishMaintenance()
			if err != nil || replay || subject.StatusValue() != agent.StatusIdle {
				t.Fatalf("replay: %v, %v", replay, err)
			}
		})
	}
}
