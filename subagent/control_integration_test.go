package subagent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent/tools/control"
	subagentdelegation "github.com/gorenx/goren/subagent/tools/delegation"
	"github.com/gorenx/goren/tools"
)

func TestSendMessageResumesColdContinuableChild(t *testing.T) {
	state, durability, backend := newContinuableIntegrationFixture(
		t,
		[][]llm.StreamChunk{
			continuableTextResponse("initial child answer"),
			continuableTextResponse("parent acknowledged first settlement"),
			continuableTextResponse("follow-up child answer"),
			continuableTextResponse("parent acknowledged second settlement"),
		},
		control.New(),
	)
	parentHandle := state.createParent(t)
	started := state.toolRuntime.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:     "start-cold-child",
			RootCallID: "start-cold-child",
			Name:       subagentdelegation.DefaultToolName,
			Arguments: json.RawMessage(`{
  "description": "resume durable child",
  "prompt": "Complete the initial child turn."
}`),
			Subject: parentHandle.Subject,
		},
	)
	if started.Failed() {
		failure, _ := started.FailureDetail()
		t.Fatalf("continuable start failed: %#v", failure)
	}
	startValue, found := started.SuccessValue()
	if !found {
		t.Fatal("continuable start returned no value")
	}
	var startResult struct {
		SubagentID string `json:"subagentId"`
	}
	if decodeErr := json.Unmarshal(startValue, &startResult); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	childID := session.SessionID(startResult.SubagentID)
	if childID == "" {
		t.Fatal("continuable start returned an empty child id")
	}
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		3*time.Second,
	)
	defer cancelWait()
	waitForContinuableSettlement(t, state, parentHandle, waitContext)
	if _, live := state.agents.Get(childID); live {
		t.Fatal("first continuable execution did not become cold")
	}
	firstInspection, inspectErr := durability.sessions.Inspect(waitContext, childID)
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	messageArguments, encodeErr := json.Marshal(struct {
		SubagentID string `json:"subagent_id"`
		Message    string `json:"message"`
	}{
		SubagentID: string(childID),
		Message:    "Complete the follow-up child turn.",
	})
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	followupOutcome := state.toolRuntime.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:     "resume-cold-child",
			RootCallID: "resume-cold-child",
			Name:       "send_message",
			Arguments:  messageArguments,
			Subject:    parentHandle.Subject,
		},
	)
	if followupOutcome.Failed() {
		failure, _ := followupOutcome.FailureDetail()
		t.Fatalf("send_message failed: %#v", failure)
	}
	followupValue, found := followupOutcome.SuccessValue()
	if !found {
		t.Fatal("send_message returned no value")
	}
	var followupResult struct {
		MessageID string `json:"messageId"`
	}
	if decodeErr := json.Unmarshal(followupValue, &followupResult); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if followupResult.MessageID == "" {
		t.Fatal("send_message returned an empty message id")
	}
	waitForContinuableSettlement(t, state, parentHandle, waitContext)
	if _, live := state.agents.Get(childID); live {
		t.Fatal("resumed continuable execution did not become cold")
	}
	secondInspection, inspectErr := durability.sessions.Inspect(waitContext, childID)
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	if len(secondInspection.Events) <= len(firstInspection.Events) {
		t.Fatalf(
			"durable event count did not advance: %d -> %d",
			len(firstInspection.Events),
			len(secondInspection.Events),
		)
	}
	requests := backend.snapshots()
	if len(requests) != 4 {
		t.Fatalf("cold-resume model request count = %d, want 4", len(requests))
	}
	if got := lastUserText(requests[2].Messages); got != "Complete the follow-up child turn." {
		t.Fatalf("resumed child prompt = %q", got)
	}
	starts, ends := state.lifecycle.snapshot()
	if len(starts) != 2 || len(ends) != 2 ||
		starts[0].ID != childID || starts[1].ID != childID ||
		ends[0].ID != childID || ends[1].ID != childID ||
		starts[0].RunID == starts[1].RunID {
		t.Fatalf("cold-resume lifecycle = %#v / %#v", starts, ends)
	}
	if failures := durability.failures.snapshot(); len(failures) != 0 {
		t.Fatalf("background persistence failures = %#v", failures)
	}
}

func TestConcurrentMessagesShareOneColdContinuableEpoch(t *testing.T) {
	resumeGate := make(chan struct{})
	state, durability, backend := newContinuableIntegrationFixture(
		t,
		[][]llm.StreamChunk{
			continuableTextResponse("initial child answer"),
			continuableTextResponse("parent acknowledged initial settlement"),
			continuableTextResponse("first resumed answer"),
			continuableTextResponse("second resumed answer"),
			continuableTextResponse("parent acknowledged resumed settlement"),
		},
		control.New(),
	)
	backend.setGates([]<-chan struct{}{
		nil,
		nil,
		resumeGate,
	})
	parentHandle := state.createParent(t)
	started := state.toolRuntime.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:     "start-concurrent-resume-child",
			RootCallID: "start-concurrent-resume-child",
			Name:       subagentdelegation.DefaultToolName,
			Arguments: json.RawMessage(`{
  "description": "accept concurrent follow-ups",
  "prompt": "Complete the initial child turn."
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
	if err := json.Unmarshal(rawStart, &startResult); err != nil {
		t.Fatal(err)
	}
	childID := session.SessionID(startResult.SubagentID)
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		3*time.Second,
	)
	defer cancelWait()
	waitForContinuableSettlement(t, state, parentHandle, waitContext)
	if _, live := state.agents.Get(childID); live {
		t.Fatal("initial Continuable epoch did not become cold")
	}
	before, err := durability.sessions.Inspect(waitContext, childID)
	if err != nil {
		t.Fatal(err)
	}

	type sendResult struct {
		index   int
		outcome tools.ToolExecutionResult
	}
	results := make(chan sendResult, 2)
	argumentsByIndex := make([]json.RawMessage, 2)
	for index := 0; index < 2; index++ {
		arguments, encodeErr := json.Marshal(struct {
			SubagentID string `json:"subagent_id"`
			Message    string `json:"message"`
		}{
			SubagentID: string(childID),
			Message: fmt.Sprintf(
				"Handle concurrent follow-up %d.",
				index+1,
			),
		})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		argumentsByIndex[index] = arguments
	}
	for index := 0; index < 2; index++ {
		indexValue := index
		arguments := argumentsByIndex[index]
		go func() {
			callID := fmt.Sprintf("concurrent-resume-%d", indexValue+1)
			results <- sendResult{
				index: indexValue,
				outcome: state.toolRuntime.Execute(
					context.Background(),
					tools.ToolExecutionInput{
						CallID:     agentmessage.CallID(callID),
						RootCallID: agentmessage.CallID(callID),
						Name:       "send_message",
						Arguments:  arguments,
						Subject:    parentHandle.Subject,
					},
				),
			}
		}()
	}
	for range 2 {
		select {
		case result := <-results:
			if result.outcome.Failed() {
				failure, _ := result.outcome.FailureDetail()
				t.Fatalf("concurrent send_message %d failed: %#v", result.index, failure)
			}
		case <-waitContext.Done():
			t.Fatal(context.Cause(waitContext))
		}
	}
	if err = backend.waitForRequests(waitContext, 3); err != nil {
		t.Fatal(err)
	}
	if requests := backend.snapshots(); len(requests) != 3 {
		t.Fatalf(
			"concurrent admission started overlapping child turns: requests=%d",
			len(requests),
		)
	}
	close(resumeGate)
	waitForContinuableSettlement(t, state, parentHandle, waitContext)
	if _, live := state.agents.Get(childID); live {
		t.Fatal("resumed Continuable epoch did not become cold")
	}
	after, err := durability.sessions.Inspect(waitContext, childID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Events) <= len(before.Events) {
		t.Fatalf(
			"durable event count did not advance: %d -> %d",
			len(before.Events),
			len(after.Events),
		)
	}
	requests := backend.snapshots()
	if len(requests) != 5 {
		t.Fatalf("concurrent cold-resume requests = %d, want 5", len(requests))
	}
	starts, ends := state.lifecycle.snapshot()
	if len(starts) != 2 || len(ends) != 2 ||
		starts[0].ID != childID || starts[1].ID != childID ||
		ends[0].ID != childID || ends[1].ID != childID ||
		starts[0].RunID == starts[1].RunID {
		t.Fatalf("concurrent cold-resume lifecycle = %#v / %#v", starts, ends)
	}
	if failures := durability.failures.snapshot(); len(failures) != 0 {
		t.Fatalf("background persistence failures = %#v", failures)
	}
}
