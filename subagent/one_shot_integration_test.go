package subagent_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/spawn"
	subagentdelegation "github.com/gorenx/goren/subagent/tools/delegation"
	"github.com/gorenx/goren/subagent/tools/report"
	"github.com/gorenx/goren/tools"
)

func TestForegroundOneShotRunsChildAndReleasesIt(t *testing.T) {
	state := newIntegrationFixture(
		t,
		[][]llm.StreamChunk{
			{
				llm.BlockEndChunk{
					Index: 0,
					Block: agentmessage.NewTextBlock("delegated answer"),
				},
				llm.FinishChunk{
					Reason: llm.StopFinish{},
				},
			},
		},
	)
	parentHandle := state.createParent(t)
	outcome := state.toolRuntime.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:     "delegate-1",
			RootCallID: "delegate-1",
			Name:       subagentdelegation.DefaultToolName,
			Arguments: json.RawMessage(`{
  "description": "answer delegated question",
  "prompt": "Return the delegated answer."
}`),
			Subject: parentHandle.Subject,
		},
	)
	if outcome.Failed() {
		failure, _ := outcome.FailureDetail()
		t.Fatalf("foreground delegation failed: %#v", failure)
	}
	rawValue, found := outcome.SuccessValue()
	if !found {
		t.Fatal("foreground delegation returned no success value")
	}
	var result struct {
		Kind   string `json:"kind"`
		RunID  string `json:"runId"`
		Output []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"output"`
	}
	if decodeErr := json.Unmarshal(rawValue, &result); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if result.Kind != "foreground" || result.RunID == "" {
		t.Fatalf("foreground result identity = %#v", result)
	}
	if len(result.Output) != 1 ||
		result.Output[0].Type != "text" ||
		result.Output[0].Text != "delegated answer" {
		t.Fatalf("foreground output = %#v", result.Output)
	}
	eventContext, cancelEventWait := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer cancelEventWait()
	if eventErr := state.lifecycle.waitForEnd(eventContext); eventErr != nil {
		t.Fatal(eventErr)
	}
	starts, ends := state.lifecycle.snapshot()
	if len(starts) != 1 || len(ends) != 1 {
		t.Fatalf("subagent lifecycle counts = %d/%d", len(starts), len(ends))
	}
	childID := starts[0].ID
	if _, childFound := state.agents.Get(childID); childFound {
		t.Fatal("completed one-shot child remains in Agent Registry")
	}
	if _, childFound := state.sessions.Get(childID); childFound {
		t.Fatal("completed one-shot child remains in LiveStore")
	}
	if liveAgents := state.agents.List(); len(liveAgents) != 1 ||
		liveAgents[0] != parentHandle.Subject {
		t.Fatalf("live Agents after one-shot = %#v", liveAgents)
	}
	requests := state.backend.snapshots()
	if len(requests) != 1 {
		t.Fatalf("model request count = %d, want 1", len(requests))
	}
	if got := lastUserText(requests[0].Messages); got != "Return the delegated answer." {
		t.Fatalf("child prompt = %q", got)
	}
	if string(starts[0].RunID) != result.RunID ||
		starts[0].RunID != ends[0].RunID ||
		starts[0].ID != ends[0].ID ||
		starts[0].Provider != spawn.DefaultSeedBuilderName ||
		ends[0].StopReason != subagent.StopCompleted {
		t.Fatalf("subagent lifecycle = %#v / %#v", starts[0], ends[0])
	}
	if failures := state.eventFailures.snapshot(); len(failures) != 0 {
		t.Fatalf("event observer failures = %#v", failures)
	}
}

func TestOneShotChildCanReportThroughItsParentAgent(t *testing.T) {
	reportPlugin, reportErr := report.New(report.Quiet)
	if reportErr != nil {
		t.Fatal(reportErr)
	}
	state := newIntegrationFixture(
		t,
		[][]llm.StreamChunk{
			{
				llm.BlockEndChunk{
					Index: 0,
					Block: agentmessage.ToolCallBlock{
						ID:        "one-shot-report",
						Name:      "report",
						Arguments: `{"output":"one-shot progress"}`,
					},
				},
				llm.FinishChunk{
					Reason: llm.ToolCallsFinish{},
				},
			},
			{
				llm.BlockEndChunk{
					Index: 0,
					Block: agentmessage.NewTextBlock("one-shot final answer"),
				},
				llm.FinishChunk{
					Reason: llm.StopFinish{},
				},
			},
		},
		reportPlugin,
	)
	parentHandle := state.createParent(t)
	outcome := state.toolRuntime.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:     "delegate-reporting-one-shot",
			RootCallID: "delegate-reporting-one-shot",
			Name:       subagentdelegation.DefaultToolName,
			Arguments: json.RawMessage(`{
  "description": "report one-shot progress",
  "prompt": "Report progress, then return the final answer."
}`),
			Subject: parentHandle.Subject,
		},
	)
	if outcome.Failed() {
		failure, _ := outcome.FailureDetail()
		t.Fatalf("foreground delegation failed: %#v", failure)
	}
	requests := state.backend.snapshots()
	if len(requests) != 2 || !hasToolSchema(requests[0].Tools, "report") {
		t.Fatalf("one-shot report requests = %#v", requests)
	}
	starts, _ := state.lifecycle.snapshot()
	pending := parentHandle.Subject.InboxValue().NextStep()
	if len(starts) != 1 || len(pending) != 1 ||
		!hasReportSource(
			[]agentmessage.Message{
				pending[0],
			},
			"one-shot progress",
			starts[0].ID,
		) {
		t.Fatal("parent Session did not retain the OneShot child report")
	}
}

func lastUserText(messages []agentmessage.Message) string {
	for messageIndex := len(messages) - 1; messageIndex >= 0; messageIndex-- {
		messageValue := messages[messageIndex]
		if messageValue.ConversationRole() != agentmessage.RoleUser {
			continue
		}
		content := messageValue.ContentValue()
		for blockIndex := len(content) - 1; blockIndex >= 0; blockIndex-- {
			plain, matchesPlain := content[blockIndex].(agentmessage.PlainTextContent)
			if !matchesPlain {
				continue
			}
			textValue, visible := plain.PlainText()
			if visible {
				return textValue
			}
		}
	}
	return ""
}
