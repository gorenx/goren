package agentloop_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func TestDrainCompletesActiveToolAndFlushWithoutStartingQueuedTurn(t *testing.T) {
	subject, history := readyAgent(t)
	proposal, err := subject.Followup(inputMessage(t, "run tool"))
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	name := "report"
	fixture := &executionEnvironment{
		history: history,
		chunks: []llm.StreamChunk{
			llm.ToolCallDeltaChunk{
				Index:          0,
				ID:             "report-call",
				Name:           &name,
				ArgumentsDelta: `{}`,
			},
			llm.FinishChunk{
				Reason: llm.ToolCallsFinish{},
			},
		},
	}
	endpoint := &heldTool{
		fixture:   fixture,
		concludes: true,
	}
	fixture.tools = endpoint
	if err := subject.Run(fixture.services()); err != nil {
		t.Fatal(err)
	}
	fixture.drain(t)
	if endpoint.waiting == nil {
		t.Fatalf("tool did not dispatch: %v", fixture.failure)
	}
	queued := inputMessage(t, "next task")
	proposal, err = subject.Followup(queued)
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	for range 2 {
		if err := subject.Drain(true); !errors.Is(err, agentloop.ErrPending) {
			t.Fatalf("drain active tool: %v", err)
		}
	}
	if subject.Cancellation() != nil {
		t.Fatal("drain requested cancellation")
	}
	if _, err := subject.Followup(inputMessage(t, "late")); !errors.Is(err, agent.ErrClosing) {
		t.Fatalf("late input: %v", err)
	}
	endpoint.waiting.ToolDispatched(fixture.services(), nil)
	fixture.drain(t)
	if fixture.flush == nil || endpoint.released != 1 || fixture.failure != nil {
		t.Fatalf("tool failed to settle: %+v, %v", endpoint, fixture.failure)
	}
	if err := subject.CheckClose(false); !errors.Is(err, agentloop.ErrPending) {
		t.Fatalf("closed before Flush: %v", err)
	}
	receiver := fixture.flush
	fixture.flush = nil
	receiver.SessionFlushed(fixture.services(), fixture.barrier, errors.New("storage unavailable"))
	if err := subject.CheckClose(false); !errors.Is(err, agentloop.ErrPending) {
		t.Fatalf("closed after failed Flush: %v", err)
	}
	if err := subject.Run(fixture.services()); err != nil {
		t.Fatal(err)
	}
	fixture.drain(t)
	if fixture.flush == nil {
		t.Fatal("failed Flush not retryable")
	}
	fixture.flush.SessionFlushed(fixture.services(), fixture.barrier, nil)
	fixture.drain(t)
	if subject.StatusValue() != agent.StatusIdle || fixture.generated != 1 {
		t.Fatal("drain started another Turn")
	}
	if err := subject.CheckClose(false); err != nil {
		t.Fatal(err)
	}
	restored, err := agent.NewInbox(history.Header(), history.Events())
	if err != nil {
		t.Fatal(err)
	}
	pending := restored.NextTurn()
	if len(pending) != 1 || pending[0].StableID() != queued.StableID() {
		t.Fatal("queued input was consumed or lost")
	}
	counts := map[string]int{}
	for _, entry := range history.Events() {
		counts[entry.Type]++
	}
	if counts[session.ToolResultEventName] != 1 || counts[session.TurnEndEventName] != 1 {
		t.Fatalf("missing work results: %v", counts)
	}
}

func TestDrainPreservesUnstartedAndLateCommittedInput(t *testing.T) {
	for _, scenario := range []string{"idle", "reserved", "late commit", "maintenance"} {
		t.Run(scenario, func(t *testing.T) {
			subject, history := readyAgent(t)
			if scenario == "maintenance" {
				if err := subject.BeginMaintenance(); err != nil {
					t.Fatal(err)
				}
			}
			message := inputMessage(t, "pending")
			proposal, err := subject.Followup(message)
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "late commit" && scenario != "idle" {
				confirmInput(t, subject, history, proposal)
			}
			err = subject.Drain(false)
			if scenario == "maintenance" {
				if !errors.Is(err, agentloop.ErrPending) {
					t.Fatal(err)
				}
				replay, failure := subject.FinishMaintenance()
				if failure != nil || replay {
					t.Fatalf("maintenance restarted: %t %v", replay, failure)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if scenario == "late commit" {
				confirmInput(t, subject, history, proposal)
			}
			if subject.StatusValue() != agent.StatusIdle || subject.Cancellation() != nil {
				t.Fatal("drain woke or cancelled work")
			}
			if err := subject.Run((&executionEnvironment{
				history: history,
			}).services()); err != nil {
				t.Fatal(err)
			}
			if err := subject.CheckClose(false); err != nil {
				t.Fatal(err)
			}
			if scenario != "idle" && !subject.InboxValue().HasPending() {
				t.Fatal("pending input lost")
			}
			for _, attempt := range []func() error{
				func() error { _, err := subject.Followup(message); return err },
				func() error { _, err := subject.Steer(message); return err },
				func() error { _, err := subject.Inject(message); return err },
				func() error { _, err := subject.Replace(message.StableID(), message); return err },
				func() error { _, err := subject.Promote(message.StableID()); return err },
				subject.BeginMaintenance,
			} {
				if err := attempt(); !errors.Is(err, agent.ErrClosing) {
					t.Fatalf("new work accepted: %v", err)
				}
			}
			if err := subject.Release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDrainDoesNotRevokeExistingCancellation(t *testing.T) {
	subject, history := readyAgent(t)
	proposal, err := subject.Followup(inputMessage(t, "cancelled"))
	if err != nil {
		t.Fatal(err)
	}
	confirmInput(t, subject, history, proposal)
	started, err := subject.PrepareTurn()
	if err != nil {
		t.Fatal(err)
	}
	write, err := history.Commit(context.Background(), startPlan{
		fact: started,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmTurn(write); err != nil {
		t.Fatal(err)
	}
	if _, err := subject.Cancel(agent.UserCancel{}, agent.CancelOptions{
		KeepInbox: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := subject.Drain(true); !errors.Is(err, agentloop.ErrPending) {
		t.Fatal(err)
	}
	if subject.Cancellation() == nil {
		t.Fatal("drain revoked cancellation")
	}
}
