package continuation

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestNaturalSettlementMapsTeardownFailureToTerminalError(t *testing.T) {
	fixture := newManagerFixture(t)
	fixture.agents.disposeErr = errors.New("handle release failed")
	childID := session.SessionID("settlement-failure")
	startResult, startErr := fixture.manager.Start(
		context.Background(),
		subagent.ContinuableStartSpec{
			Provider: "spawn",
			Label:    "review",
			ChildID:  &childID,
			Request: subagent.ContinuableRequest{
				Parent: fixture.parent,
				Prompt: []llm.ContentBlock{
					llm.NewTextBlock("finish naturally"),
				},
			},
		},
	)
	if startErr != nil {
		t.Fatal(startErr)
	}
	childAgent, found := fixture.agents.Get(childID)
	if !found {
		t.Fatal("child was not published")
	}
	fixture.manager.MessageLeftInbox(childAgent, startResult.MessageID)
	childAgent.(*agentRecord).becomeIdle()

	deadline := time.Now().Add(time.Second)
	for len(fixture.lifecycle.endedSnapshot()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("natural settlement did not publish its terminal edge")
		}
		runtime.Gosched()
	}
	terminalFacts := fixture.lifecycle.endedSnapshot()
	if len(terminalFacts) != 1 ||
		terminalFacts[0].ID != childID ||
		terminalFacts[0].StopReason != subagent.StopError {
		t.Fatalf("terminal lifecycle = %#v", terminalFacts)
	}
	parentMessages := fixture.parent.messagesSnapshot()
	if len(parentMessages) != 1 {
		t.Fatalf("parent settlement messages = %d", len(parentMessages))
	}
	content := parentMessages[0].ContentValue()
	if len(content) == 0 ||
		content[0].(llm.TextBlock).Text !=
			"Background subagent settlement-failure failed before it finished." {
		t.Fatalf("settlement content = %#v", content)
	}
	if parentMessages[0].SourceValue().SourceKind() != "subagent-settled" {
		t.Fatalf("settlement source = %#v", parentMessages[0].SourceValue())
	}
	if _, found := fixture.agents.Get(childID); found {
		t.Fatal("failed natural settlement retained the child")
	}
}
