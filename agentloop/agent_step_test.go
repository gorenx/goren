package agentloop_test

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

func prepareStep(t *testing.T) (*agentloop.ReactLoopAgent, *session.Log, session.EventDraft) {
	t.Helper()
	subject, history := openTurn(t)
	edit, err := subject.PrepareInput()
	if err != nil {
		t.Fatal(err)
	}
	write, err := history.Commit(context.Background(), session.Batch(edit.Drafts...))
	if err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmInput(write); err != nil {
		t.Fatal(err)
	}
	admission, err := subject.PreparePreStep()
	if err != nil {
		t.Fatal(err)
	}
	if accepted, err := subject.AcceptPreStep(admission.Default); err != nil || !accepted {
		t.Fatalf("admission: %v, %v", accepted, err)
	}
	draft, err := subject.PrepareStep(agentloop.Preparation{
		Snapshot: history.Snapshot(),
	})
	if err != nil || draft == nil {
		t.Fatalf("prepare Step: %v, %v", draft, err)
	}
	return subject, history, *draft
}

func TestStepStateRejectsEarlyExecutionAndForeignConfirmations(t *testing.T) {
	subject, history, draft := prepareStep(t)
	if err := subject.BeginRequest(); err == nil {
		t.Fatal("model began before Step start committed")
	}
	if _, err := subject.MessageDraft(); err == nil {
		t.Fatal("message emitted before Step start committed")
	}
	write, err := history.Commit(context.Background(), session.Batch(draft))
	if err != nil {
		t.Fatal(err)
	}
	foreign := write
	foreign.Barrier.SessionID = "other"
	if err := subject.ConfirmStep(foreign); err == nil {
		t.Fatal("foreign start accepted")
	}
	if err := subject.ConfirmStep(write); err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmStep(write); err == nil {
		t.Fatal("start confirmed twice")
	}
	if err := subject.BeginRequest(); err == nil {
		t.Fatal("model began before input committed")
	}
	input, err := subject.MessageDraft()
	if err != nil || input == nil {
		t.Fatalf("message: %v", err)
	}
	write, err = history.Commit(context.Background(), session.Batch(*input))
	if err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmMessage(write); err != nil {
		t.Fatal(err)
	}
	if err := subject.BeginRequest(); err != nil {
		t.Fatal(err)
	}
	end, err := subject.PrepareStepEnd(stepstate.StepResultError)
	if err != nil {
		t.Fatal(err)
	}
	write, err = history.Commit(context.Background(), session.Batch(end))
	if err != nil {
		t.Fatal(err)
	}
	foreign = write
	foreign.Barrier.SessionID = "other"
	if err := subject.ConfirmStepEnd(foreign); err == nil {
		t.Fatal("foreign end accepted")
	}
	if _, err := subject.PrepareTurnEnd(nil); err == nil {
		t.Fatal("Turn ended before Step confirmation")
	}
	if err := subject.ConfirmStepEnd(write); err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmStepEnd(write); err == nil {
		t.Fatal("end confirmed twice")
	}
}

func TestStepRecoveryUsesProposedStateAndRetainsConfirmedWork(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "not committed", true: "committed"}[committed], func(t *testing.T) {
			subject, history, draft := prepareStep(t)
			if committed {
				if _, err := history.Commit(context.Background(), session.Batch(draft)); err != nil {
					t.Fatal(err)
				}
			}
			found, err := subject.ReconcileStepStart(history.Snapshot())
			if err != nil || found != committed {
				t.Fatalf("reconcile = %v, %v", found, err)
			}
			if committed {
				if _, err := subject.PrepareTurnEnd(nil); err == nil {
					t.Fatal("recovered Step did not block Turn end")
				}
				if _, err := subject.ReconcileStepStart(history.Snapshot()); err == nil {
					t.Fatal("opened Step reconciled again")
				}
			} else {
				if _, err := subject.PrepareTurnEnd(context.Canceled); err != nil {
					t.Fatal("uncommitted candidate blocked settlement", err)
				}
			}
		})
	}
}

func TestPreparationRejectsSkippedAndRepeatedTransitions(t *testing.T) {
	subject, history := openTurn(t)
	edit, err := subject.PrepareInput()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := subject.PrepareInput(); err == nil {
		t.Fatal("input claim prepared twice")
	}
	if _, err := subject.PreparePreStep(); err == nil {
		t.Fatal("context prepared before claim confirmation")
	}
	if err := subject.AbandonPreparation(); err == nil {
		t.Fatal("unconfirmed claim discarded")
	}
	write, err := history.Commit(context.Background(), session.Batch(edit.Drafts...))
	if err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmInput(write); err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmInput(write); err == nil {
		t.Fatal("claim confirmed twice")
	}
	prepared, err := subject.PreparePreStep()
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Input.Messages) != 1 {
		t.Fatal("confirmed claim missing from middleware input")
	}
	if _, err := subject.PreparePreStep(); err == nil {
		t.Fatal("pending admission replaced")
	}
	if err := subject.AbandonPreparation(); err != nil {
		t.Fatal(err)
	}
	if _, err := subject.AcceptPreStep(prepared.Default); err == nil {
		t.Fatal("late admission accepted after cancellation")
	}
}

func TestPreStepDecisionPrecedesContextAndRetainsDetachedMessages(t *testing.T) {
	subject, history := openTurn(t)
	edit, err := subject.PrepareInput()
	if err != nil {
		t.Fatal(err)
	}
	write, err := history.Commit(context.Background(), session.Batch(edit.Drafts...))
	if err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmInput(write); err != nil {
		t.Fatal(err)
	}
	proposal, err := subject.PreparePreStep()
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Default.Messages) != 1 {
		t.Fatal("default must contain claimed input only")
	}
	provided := agentloop.Preparation{
		Snapshot: history.Snapshot(),

		Prompt: systemprompt.PromptAssembly{
			Contexts: []systemprompt.AssembledContext{
				{
					Name: "workspace",
					Text: "cwd: /workspace",
				},
			},
		},
	}
	if _, err := subject.PrepareStep(provided); err == nil {
		t.Fatal("context prepared before middleware returned")
	}
	decision := agent.PreStepDecision{
		Kind:     agent.PreStepEnter,
		Messages: []agentmessage.UserMessage{inputMessage(t, "rewritten")},
	}
	if accepted, err := subject.AcceptPreStep(decision); err != nil || !accepted {
		t.Fatalf("admission: %v, %v", accepted, err)
	}
	admittedID := decision.Messages[0].StableID()
	decision.Messages[0] = inputMessage(t, "external replacement")
	if _, err := subject.AcceptPreStep(proposal.Default); err == nil {
		t.Fatal("duplicate admission replaced input")
	}
	draft, err := subject.PrepareStep(provided)
	if err != nil || draft == nil {
		t.Fatalf("Step: %v, %v", draft, err)
	}
	write, err = history.Commit(context.Background(), session.Batch(*draft))
	if err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmStep(write); err != nil {
		t.Fatal(err)
	}
	for index := range 2 {
		input, err := subject.MessageDraft()
		if err != nil || input == nil {
			t.Fatalf("admitted input or context missing: %v", err)
		}
		write, err = history.Commit(context.Background(), session.Batch(*input))
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			entered, err := agentmessage.DecodeUserMessage(write.Events[0].Data)
			if err != nil {
				t.Fatal(err)
			}
			if entered.StableID() != admittedID {
				t.Fatal("middleware decision was replaced after admission")
			}
		}
		if err := subject.ConfirmMessage(write); err != nil {
			t.Fatal(err)
		}
	}
	if input, err := subject.MessageDraft(); err != nil || input != nil {
		t.Fatalf("extra input: %v, %v", input, err)
	}
}
