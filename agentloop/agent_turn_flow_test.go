package agentloop_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	"github.com/gorenx/goren/session"
)

func requestingStep(t *testing.T) (*agentloop.ReactLoopAgent, *session.Log) {
	t.Helper()
	subject, history, draft := prepareStep(t)
	write, err := history.Commit(context.Background(), session.Batch(draft))
	if err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmStep(write); err != nil {
		t.Fatal(err)
	}
	for {
		message, err := subject.MessageDraft()
		if err != nil {
			t.Fatal(err)
		}
		if message == nil {
			break
		}
		write, err = history.Commit(context.Background(), session.Batch(*message))
		if err != nil {
			t.Fatal(err)
		}
		if err := subject.ConfirmMessage(write); err != nil {
			t.Fatal(err)
		}
	}
	if err := subject.BeginRequest(); err != nil {
		t.Fatal(err)
	}
	return subject, history
}

func completedStep(t *testing.T) (*agentloop.ReactLoopAgent, *session.Log) {
	t.Helper()
	subject, history := requestingStep(t)
	draft, err := subject.PrepareStepEnd(stepstate.StepResultCompleted)
	if err != nil {
		t.Fatal(err)
	}
	write, err := history.Commit(context.Background(), stepEndPlan{fact: draft})
	if err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmStepEnd(write); err != nil {
		t.Fatal(err)
	}
	return subject, history
}

func turnReason(t *testing.T, history *session.Log, action agentloop.TurnOperation) session.TurnEndReason {
	t.Helper()
	ending, ok := action.(agentloop.TurnEnd)
	if !ok {
		t.Fatalf("expected Turn end, got %T", action)
	}
	write, err := history.Commit(context.Background(), endingPlan{
		fact: ending,
	})
	if err != nil || len(write.Events) != 1 {
		t.Fatalf("end commit: %+v, %v", write, err)
	}
	var boundary session.TurnEnd
	if err := json.Unmarshal(write.Events[0].Data, &boundary); err != nil {
		t.Fatal(err)
	}
	return boundary.Reason
}

func TestStoppingUsesCommittedInputAndOwnedCancellation(t *testing.T) {
	for _, scenario := range []string{"complete", "steer", "cancel before steer", "cancel after steer", "hook error"} {
		t.Run(scenario, func(t *testing.T) {
			subject, history := completedStep(t)
			if _, err := subject.CompleteStopping(agent.TurnStoppingCompleted{}); err == nil {
				t.Fatal("unrequested completion accepted")
			}
			next, err := subject.ContinueTurn(nil)
			if _, ok := next.(agentloop.RunTurnStopping); err != nil || !ok {
				t.Fatalf("stopping boundary: %T, %v", next, err)
			}
			if _, err := subject.ContinueTurn(nil); err == nil {
				t.Fatal("stopping hook requested twice")
			}
			cancel := func() {
				if _, err := subject.Cancel(agent.ParentCancel{}, agent.CancelOptions{
					KeepInbox: true,
				}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "cancel before steer" {
				cancel()
			}
			if scenario != "complete" {
				proposal, err := subject.Steer(inputMessage(t, "during stopping"))
				if err != nil {
					t.Fatal(err)
				}
				confirmInput(t, subject, history, proposal)
			}
			if scenario == "cancel after steer" {
				cancel()
			}
			var failure error
			if scenario == "hook error" {
				failure = errors.New("hook failed")
			}
			next, err = subject.CompleteStopping(agent.TurnStoppingCompleted{
				Err: failure,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := subject.CompleteStopping(agent.TurnStoppingCompleted{}); err == nil {
				t.Fatal("duplicate completion accepted")
			}
			if scenario == "steer" {
				if _, ok := next.(agentloop.ContinueSteps); !ok {
					t.Fatalf("Steer did not resume: %T", next)
				}
				return
			}
			reason := turnReason(t, history, next)
			switch scenario {
			case "complete":
				if _, ok := reason.(session.TurnCompleted); !ok {
					t.Fatalf("normal end: %T", reason)
				}
			case "hook error":
				if _, ok := reason.(session.TurnError); !ok {
					t.Fatalf("hook failure end: %T", reason)
				}
			default:
				aborted, ok := reason.(session.TurnAborted)
				if !ok {
					t.Fatalf("cancel resumed execution: %T", reason)
				}
				if _, ok := aborted.Reason.(session.ParentCancelCause); !ok {
					t.Fatalf("lost cancellation cause: %T", aborted.Reason)
				}
			}
		})
	}
}

func TestContinuationReadsCancellationWithoutCallerForwarding(t *testing.T) {
	for _, direct := range []bool{false, true} {
		subject, history := completedStep(t)
		if _, err := subject.Cancel(agent.ParentCancel{}, agent.CancelOptions{
			KeepInbox: true,
		}); err != nil {
			t.Fatal(err)
		}
		var next agentloop.TurnOperation
		var err error
		if direct {
			next, err = subject.PrepareTurnEnd(nil)
		} else {
			next, err = subject.ContinueTurn(nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := turnReason(t, history, next).(session.TurnAborted); !ok {
			t.Fatal("accepted cancellation was ignored")
		}
	}
}

func TestLateCancellationPreservesPreparedEnd(t *testing.T) {
	subject, history := completedStep(t)
	if _, err := subject.PrepareTurnEnd(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := subject.Cancel(agent.UserCancel{}, agent.CancelOptions{
		KeepInbox: true,
	}); err != nil {
		t.Fatal(err)
	}
	next, err := subject.ContinueTurn(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := turnReason(t, history, next).(session.TurnCompleted); !ok {
		t.Fatal("late cancellation replaced the prepared end")
	}
}
