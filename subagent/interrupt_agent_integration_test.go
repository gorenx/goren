package subagent_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent/control"
	subagenttool "github.com/gorenx/goren/subagent/tool"
	"github.com/gorenx/goren/tools"
)

func TestInterruptAgentCancelsCurrentTurnAndKeepsQueuedFollowup(t *testing.T) {
	firstRequestGate := make(chan struct{})
	backend := &integrationAdapter{
		responses: [][]llm.StreamChunk{
			continuableTextResponse("interrupted response"),
			continuableTextResponse("parked follow-up response"),
		},
		gates: []<-chan struct{}{
			firstRequestGate,
		},
		requestsChanged: make(chan struct{}, 4),
	}
	state, _ := newContinuableIntegrationFixtureWithAdapter(
		t,
		backend,
		control.New(),
	)
	parentHandle := state.createParent(t)
	started := state.toolRuntime.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:     "start-interruptible-child",
			RootCallID: "start-interruptible-child",
			Name:       subagenttool.DefaultToolName,
			Arguments: json.RawMessage(`{
  "description": "interruptible durable child",
  "prompt": "Begin work that will be interrupted."
}`),
			Subject: parentHandle.Subject,
		},
	)
	if started.Failed() {
		failure, _ := started.FailureDetail()
		t.Fatalf("continuable start failed: %#v", failure)
	}
	rawStart, found := started.SuccessValue()
	if !found {
		t.Fatal("continuable start returned no value")
	}
	var startResult struct {
		SubagentID string `json:"subagentId"`
	}
	if decodeErr := json.Unmarshal(rawStart, &startResult); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	childID := session.SessionID(startResult.SubagentID)
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		3*time.Second,
	)
	defer cancelWait()
	if requestErr := backend.waitForRequests(waitContext, 1); requestErr != nil {
		t.Fatal(requestErr)
	}
	childAgent, live := state.agents.Get(childID)
	if !live || childAgent.StatusValue() != agent.StatusRunning {
		t.Fatalf("interrupt target = (%v, %t), want live running child", childAgent, live)
	}
	queued := executeSendMessage(
		t,
		state,
		parentHandle,
		childID,
		"Keep this follow-up parked after cancellation.",
	)
	if queued == "" {
		t.Fatal("send_message returned an empty message id")
	}
	interrupted := state.toolRuntime.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:     "interrupt-running-child",
			RootCallID: "interrupt-running-child",
			Name:       "interrupt_agent",
			Arguments: json.RawMessage(`{
  "agent_id": "` + string(childID) + `"
}`),
			Subject: parentHandle.Subject,
		},
	)
	if interrupted.Failed() {
		failure, _ := interrupted.FailureDetail()
		t.Fatalf("interrupt_agent failed: %#v", failure)
	}
	if idleErr := childAgent.WhenIdle(waitContext); idleErr != nil {
		t.Fatal(idleErr)
	}
	if requests := backend.snapshots(); len(requests) != 1 {
		t.Fatalf("interrupt resumed parked work; model requests = %d, want 1", len(requests))
	}
	resident, live := state.agents.Get(childID)
	if !live || !agent.Same(resident, childAgent) ||
		resident.StatusValue() != agent.StatusIdle {
		t.Fatalf("interrupted child residency = (%v, %t)", resident, live)
	}
	pending := resident.InboxValue().NextTurn()
	if len(pending) != 1 || pending[0].StableID() != queued {
		t.Fatalf("parked child Inbox = %#v", pending)
	}
	if failures := state.eventFailures.snapshot(); len(failures) != 0 {
		t.Fatalf("event observer failures = %#v", failures)
	}
}

func executeSendMessage(
	t *testing.T,
	state *integrationFixture,
	parentHandle agent.Handle,
	childID session.SessionID,
	messageText string,
) llm.MessageID {
	t.Helper()
	arguments, encodeErr := json.Marshal(struct {
		SubagentID string `json:"subagent_id"`
		Message    string `json:"message"`
	}{
		SubagentID: string(childID),
		Message:    messageText,
	})
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	outcome := state.toolRuntime.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:     "queue-before-interrupt",
			RootCallID: "queue-before-interrupt",
			Name:       "send_message",
			Arguments:  arguments,
			Subject:    parentHandle.Subject,
		},
	)
	if outcome.Failed() {
		failure, _ := outcome.FailureDetail()
		t.Fatalf("send_message failed: %#v", failure)
	}
	rawValue, found := outcome.SuccessValue()
	if !found {
		t.Fatal("send_message returned no value")
	}
	var result struct {
		MessageID string `json:"messageId"`
	}
	if decodeErr := json.Unmarshal(rawValue, &result); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	return llm.MessageID(result.MessageID)
}
