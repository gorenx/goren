package continuation

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestStartRollsBackActivationWhenInitialInboxAcceptanceFails(t *testing.T) {
	fixture := newManagerFixture(t)
	fixture.agents.followupErr = errors.New("inbox rejected initial prompt")
	childID := session.SessionID("rejected-start")

	_, startErr := fixture.manager.Start(
		context.Background(),
		subagent.ContinuableStartSpec{
			Provider: "spawn",
			Label:    "review",
			ChildID:  &childID,
			Request: subagent.ContinuableRequest{
				Parent: fixture.parent,
				Prompt: []llm.ContentBlock{
					llm.NewTextBlock("rejected"),
				},
			},
		},
	)
	if startErr == nil || startErr.Error() != "inbox rejected initial prompt" {
		t.Fatalf("initial acceptance failure = %v", startErr)
	}
	assertMaterializationRolledBack(t, fixture, childID)
	if messages := fixture.parent.messagesSnapshot(); len(messages) != 0 {
		t.Fatalf("unannounced rollback notified parent: %#v", messages)
	}
}

func assertMaterializationRolledBack(
	t *testing.T,
	fixture *managerFixture,
	childID session.SessionID,
) {
	t.Helper()
	if _, found := fixture.agents.Get(childID); found {
		t.Fatal("rolled-back child remained in the Agent Registry")
	}
	startFacts := fixture.lifecycle.startedSnapshot()
	terminalFacts := fixture.lifecycle.endedSnapshot()
	if len(startFacts) != 1 ||
		len(terminalFacts) != 1 ||
		startFacts[0].RunID != terminalFacts[0].RunID ||
		terminalFacts[0].StopReason != subagent.StopAborted {
		t.Fatalf(
			"rolled-back lifecycle = started %#v, ended %#v",
			startFacts,
			terminalFacts,
		)
	}
}
