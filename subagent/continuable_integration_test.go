package subagent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/spawn"
	subagentdelegation "github.com/gorenx/goren/subagent/tools/delegation"
	"github.com/gorenx/goren/tools"
)

func TestContinuableToolPersistsCompletedChildForLaterResume(t *testing.T) {
	state, durability, backend := newContinuableIntegrationFixture(
		t,
		[][]llm.StreamChunk{
			continuableTextResponse("initial child answer"),
			continuableTextResponse("parent acknowledged settlement"),
		},
	)
	parentHandle := state.createParent(t)
	outcome := state.toolRuntime.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:     "continuable-1",
			RootCallID: "continuable-1",
			Name:       subagentdelegation.DefaultToolName,
			Arguments: json.RawMessage(`{
  "description": "continue durable work",
  "prompt": "Complete the initial child turn."
}`),
			Subject: parentHandle.Subject,
		},
	)
	if outcome.Failed() {
		failure, _ := outcome.FailureDetail()
		t.Fatalf("continuable delegation failed: %#v", failure)
	}
	rawValue, found := outcome.SuccessValue()
	if !found {
		t.Fatal("continuable delegation returned no success value")
	}
	var result struct {
		Kind       string `json:"kind"`
		SubagentID string `json:"subagentId"`
	}
	if decodeErr := json.Unmarshal(rawValue, &result); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if result.Kind != "continuable" || result.SubagentID == "" {
		t.Fatalf("continuable result = %#v", result)
	}
	childID := session.SessionID(result.SubagentID)
	eventContext, cancelEventWait := context.WithTimeout(
		context.Background(),
		3*time.Second,
	)
	defer cancelEventWait()
	if eventErr := state.lifecycle.waitForEnd(eventContext); eventErr != nil {
		t.Fatal(eventErr)
	}
	if idleErr := parentHandle.Subject.WhenIdle(eventContext); idleErr != nil {
		t.Fatal(idleErr)
	}
	if _, live := state.agents.Get(childID); live {
		t.Fatal("settled continuable child remains resident")
	}
	if _, live := state.sessions.Get(childID); live {
		t.Fatal("settled continuable Session remains in LiveStore")
	}
	inspection, inspectErr := durability.sessions.Inspect(eventContext, childID)
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	if inspection.Header.ParentSession == nil ||
		*inspection.Header.ParentSession != parentHandle.Subject.ID() ||
		inspection.Header.Origin != session.OriginSubagent {
		t.Fatalf("durable child header = %#v", inspection.Header)
	}
	descriptor, descriptorFound, foldErr := subagent.FoldDescriptor(inspection.Events)
	if foldErr != nil {
		t.Fatal(foldErr)
	}
	continuableDescriptor, continuable := descriptor.(subagent.ContinuableDescriptor)
	if !descriptorFound || !continuable ||
		continuableDescriptor.Provider != spawn.DefaultSeedBuilderName ||
		continuableDescriptor.Label != "continue durable work" {
		t.Fatalf("durable descriptor = %#v, found=%t", descriptor, descriptorFound)
	}
	starts, ends := state.lifecycle.snapshot()
	if len(starts) != 1 || len(ends) != 1 ||
		starts[0].RunID != ends[0].RunID ||
		starts[0].ID != childID ||
		ends[0].ID != childID ||
		ends[0].StopReason != subagent.StopCompleted {
		t.Fatalf("continuable lifecycle = %#v / %#v", starts, ends)
	}
	requests := backend.snapshots()
	if len(requests) != 2 {
		t.Fatalf("continuable model request count = %d, want 2", len(requests))
	}
	if got := lastUserText(requests[0].Messages); got != "Complete the initial child turn." {
		t.Fatalf("continuable child prompt = %q", got)
	}
	if got := lastUserContentText(requests[1].Messages); !strings.Contains(
		got,
		"Background subagent "+result.SubagentID+" finished",
	) {
		t.Fatalf("parent settlement notice = %q", got)
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

func lastUserContentText(messages []llm.Message) string {
	for messageIndex := len(messages) - 1; messageIndex >= 0; messageIndex-- {
		messageValue := messages[messageIndex]
		if messageValue.ConversationRole() != llm.RoleUser {
			continue
		}
		var content strings.Builder
		for _, block := range messageValue.ContentValue() {
			plain, matches := block.(llm.PlainTextContent)
			if !matches {
				continue
			}
			textValue, visible := plain.PlainText()
			if visible {
				content.WriteString(textValue)
			}
		}
		return content.String()
	}
	return ""
}

func continuableTextResponse(textValue string) []llm.StreamChunk {
	return []llm.StreamChunk{
		llm.BlockEndChunk{
			Index: 0,
			Block: llm.NewTextBlock(textValue),
		},
		llm.FinishChunk{
			Reason: llm.StopFinish{},
		},
	}
}
