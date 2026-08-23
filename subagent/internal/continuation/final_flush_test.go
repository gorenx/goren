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

func TestFinalFlushFailureIsReportedWithoutRetainingActivation(t *testing.T) {
	fixture := newManagerFixture(t)
	flushFailure := errors.New("storage unavailable")
	fixture.sessions.flushErr = flushFailure
	childID := session.SessionID("flush-failure")
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
		terminalFacts[0].StopReason != subagent.StopCompleted {
		t.Fatalf("terminal lifecycle = %#v", terminalFacts)
	}
	failures := fixture.failures.failuresSnapshot()
	if len(failures) != 1 ||
		failures[0].ChildID != childID ||
		!errors.Is(failures[0].Error, flushFailure) {
		t.Fatalf("reported final flush failures = %#v", failures)
	}
	if _, found := fixture.agents.Get(childID); found {
		t.Fatal("best-effort flush failure retained the child")
	}
}
