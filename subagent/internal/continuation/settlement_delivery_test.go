package continuation

import (
	"context"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestFollowupWaitsForNaturalSettlementAndResumesDurableChild(t *testing.T) {
	fixture := newManagerFixture(t)
	childID := session.SessionID("settling-child")
	startResult, startErr := fixture.manager.Start(
		context.Background(),
		subagent.ContinuableStartSpec{
			Provider: "spawn",
			Label:    "settlement delivery",
			ChildID:  &childID,
			Request: subagent.ContinuableRequest{
				Parent: fixture.parent,
				Prompt: []llm.ContentBlock{
					llm.NewTextBlock("first turn"),
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
	statusRead := make(chan struct{}, 1)
	resumeStatus := make(chan struct{})
	childRecord := childAgent.(*agentRecord)
	childRecord.pauseStatusRead(statusRead, resumeStatus)
	fixture.manager.MessageLeftInbox(childAgent, startResult.MessageID)
	childRecord.becomeIdle()
	select {
	case <-statusRead:
	case <-time.After(time.Second):
		t.Fatal("settlement did not reach its final idle observation")
	}

	type followupResult struct {
		messageID llm.MessageID
		err       error
	}
	followupStarted := make(chan struct{})
	followupDone := make(chan followupResult, 1)
	go func() {
		close(followupStarted)
		messageID, followupErr := fixture.manager.Followup(
			context.Background(),
			fixture.parent,
			childID,
			[]llm.ContentBlock{
				llm.NewTextBlock("second turn"),
			},
			subagent.FollowupOptions{
				Source: subagent.CoordinatorSource{
					SenderSessionID: fixture.parent.ID(),
				},
			},
		)
		followupDone <- followupResult{
			messageID: messageID,
			err:       followupErr,
		}
	}()
	<-followupStarted
	select {
	case result := <-followupDone:
		t.Fatalf("Followup crossed an unsettled Activation boundary: %#v", result)
	case <-time.After(20 * time.Millisecond):
	}
	close(resumeStatus)

	var delivered followupResult
	select {
	case delivered = <-followupDone:
	case <-time.After(time.Second):
		t.Fatal("Followup did not resume after settlement completed")
	}
	if delivered.err != nil || delivered.messageID == "" {
		t.Fatalf("resumed Followup = %q, %v", delivered.messageID, delivered.err)
	}
	resumedAgent, found := fixture.agents.Get(childID)
	if !found || resumedAgent == childAgent {
		t.Fatal("Followup did not resume a new Activation epoch")
	}
	resumedMessages := resumedAgent.(*agentRecord).messagesSnapshot()
	if len(resumedMessages) != 1 ||
		resumedMessages[0].ContentValue()[0].(llm.TextBlock).Text != "second turn" {
		t.Fatalf("resumed messages = %#v", resumedMessages)
	}
	startFacts := fixture.lifecycle.startedSnapshot()
	terminalFacts := fixture.lifecycle.endedSnapshot()
	if len(startFacts) != 2 ||
		len(terminalFacts) != 1 ||
		startFacts[0].RunID == startFacts[1].RunID ||
		startFacts[0].RunID != terminalFacts[0].RunID ||
		terminalFacts[0].StopReason != subagent.StopCompleted {
		t.Fatalf("settlement epochs = %#v / %#v", startFacts, terminalFacts)
	}
	if drainErr := fixture.manager.DrainChildren(
		context.Background(),
		fixture.parent,
		[]session.SessionID{childID},
	); drainErr != nil {
		t.Fatal(drainErr)
	}
}
