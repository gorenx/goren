package subagent_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent/control"
	subagenttool "github.com/gorenx/goren/subagent/tool"
	"github.com/gorenx/goren/tools"
)

func TestListAgentsProjectsColdContinuableChild(t *testing.T) {
	state, _, _ := newContinuableIntegrationFixture(
		t,
		[][]llm.StreamChunk{
			continuableTextResponse("child completed for listing"),
			continuableTextResponse("parent acknowledged listed child"),
		},
		control.New(),
	)
	parentHandle := state.createParent(t)
	started := state.toolRuntime.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:     "start-listed-child",
			RootCallID: "start-listed-child",
			Name:       subagenttool.DefaultToolName,
			Arguments: json.RawMessage(`{
  "description": "listed durable child",
  "prompt": "Complete this child turn for listing."
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
	waitForContinuableSettlement(t, state, parentHandle, waitContext)
	if _, live := state.agents.Get(childID); live {
		t.Fatal("listed child did not become cold")
	}
	listed := state.toolRuntime.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:     "list-cold-child",
			RootCallID: "list-cold-child",
			Name:       "list_agents",
			Arguments:  json.RawMessage(`{}`),
			Subject:    parentHandle.Subject,
		},
	)
	if listed.Failed() {
		failure, _ := listed.FailureDetail()
		t.Fatalf("list_agents failed: %#v", failure)
	}
	rawList, found := listed.SuccessValue()
	if !found {
		t.Fatal("list_agents returned no value")
	}
	var entries []struct {
		Kind   string `json:"kind"`
		ID     string `json:"id"`
		Label  string `json:"label"`
		Status string `json:"status"`
	}
	if decodeErr := json.Unmarshal(rawList, &entries); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(entries) != 1 ||
		entries[0].Kind != "child" ||
		entries[0].ID != string(childID) ||
		entries[0].Label != "listed durable child" ||
		entries[0].Status != "ready" {
		t.Fatalf("list_agents entries = %#v", entries)
	}
}
