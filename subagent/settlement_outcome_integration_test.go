package subagent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/subagent"
	subagenttool "github.com/gorenx/goren/subagent/tool"
	"github.com/gorenx/goren/tools"
)

func TestContinuableSettlementReportsMaxTokensFromAgentLog(t *testing.T) {
	assertContinuableSettlementOutcome(
		t,
		[]llm.StreamChunk{
			llm.BlockEndChunk{
				Index: 0,
				Block: llm.NewTextBlock("partial answer"),
			},
			llm.FinishChunk{
				Reason: llm.MaxTokensFinish{},
			},
		},
		subagent.StopMaxTokens,
		"ran out of room before it finished",
		"partial answer",
	)
}

func TestContinuableSettlementReportsModelFailureFromAgentLog(t *testing.T) {
	assertContinuableSettlementOutcome(
		t,
		[]llm.StreamChunk{
			llm.FinishChunk{
				Reason: llm.ErrorFinish{
					Failure: llm.LlmFailure{
						Code:    "MODEL",
						Message: "provider failed",
					},
				},
			},
		},
		subagent.StopError,
		"failed before it finished",
		"",
	)
}

func assertContinuableSettlementOutcome(
	t *testing.T,
	childResponse []llm.StreamChunk,
	expected subagent.StopReason,
	settlementText string,
	settlementOutput string,
) {
	t.Helper()
	state, durability, backend := newContinuableIntegrationFixture(
		t,
		[][]llm.StreamChunk{
			childResponse,
			continuableTextResponse("parent acknowledged settlement"),
		},
	)
	parentHandle := state.createParent(t)
	execution := state.toolRuntime.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:     "continuable-outcome",
			RootCallID: "continuable-outcome",
			Name:       subagenttool.DefaultToolName,
			Arguments: json.RawMessage(`{
  "description": "observe terminal outcome",
  "prompt": "Run the child turn."
}`),
			Subject: parentHandle.Subject,
		},
	)
	if execution.Failed() {
		failure, _ := execution.FailureDetail()
		t.Fatalf("continuable delegation failed: %#v", failure)
	}
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		3*time.Second,
	)
	defer cancelWait()
	waitForContinuableSettlement(t, state, parentHandle, waitContext)

	starts, ends := state.lifecycle.snapshot()
	if len(starts) != 1 || len(ends) != 1 ||
		starts[0].RunID != ends[0].RunID ||
		ends[0].StopReason != expected {
		t.Fatalf("continuable lifecycle = %#v / %#v", starts, ends)
	}
	requests := backend.snapshots()
	if len(requests) != 2 {
		t.Fatalf("model request count = %d, want 2", len(requests))
	}
	parentInput := lastUserContentText(requests[1].Messages)
	if !strings.Contains(parentInput, settlementText) {
		t.Fatalf("parent settlement notice = %q", parentInput)
	}
	if settlementOutput != "" && !strings.Contains(parentInput, settlementOutput) {
		t.Fatalf("parent settlement output = %q", parentInput)
	}
	if failures := durability.failures.snapshot(); len(failures) != 0 {
		t.Fatalf("background persistence failures = %#v", failures)
	}
	if failures := state.eventFailures.snapshot(); len(failures) != 0 {
		t.Fatalf("event observer failures = %#v", failures)
	}
	if failures := state.observerErrors.snapshot(); len(failures) != 0 {
		t.Fatalf("contained Subagent failures = %#v", failures)
	}
}
