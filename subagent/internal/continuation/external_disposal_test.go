package continuation

import (
	"context"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestExternalDisposalPublishesTerminalBeforeReleasingOwnership(t *testing.T) {
	fixture := newManagerFixture(t)
	childID := session.SessionID("owner-child")
	_, startErr := fixture.manager.Start(
		context.Background(),
		subagent.ContinuableStartSpec{
			Provider: "spawn",
			Label:    "external disposal",
			ChildID:  &childID,
			Request: subagent.ContinuableRequest{
				Parent: fixture.parent,
				Prompt: []llm.ContentBlock{
					llm.NewTextBlock("hold"),
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
	grandchildID := session.SessionID("externally-disposed")
	_, startErr = fixture.manager.Start(
		context.Background(),
		subagent.ContinuableStartSpec{
			Provider: "spawn",
			Label:    "external disposal target",
			ChildID:  &grandchildID,
			Request: subagent.ContinuableRequest{
				Parent: childAgent,
				Prompt: []llm.ContentBlock{
					llm.NewTextBlock("hold"),
				},
			},
		},
	)
	if startErr != nil {
		t.Fatal(startErr)
	}
	grandchildAgent, found := fixture.agents.Get(grandchildID)
	if !found {
		t.Fatal("grandchild was not published")
	}
	terminalEntered := make(chan struct{})
	releaseTerminal := make(chan struct{})
	fixture.lifecycle.endedHook = func(agent.Agent, subagent.Ended) {
		close(terminalEntered)
		<-releaseTerminal
	}
	disposed := make(chan struct{})
	go func() {
		fixture.manager.AgentDisposed(grandchildAgent)
		close(disposed)
	}()
	select {
	case <-terminalEntered:
	case <-time.After(time.Second):
		t.Fatal("external disposal did not publish its terminal lifecycle edge")
	}

	fixture.manager.residency.mutex.Lock()
	epoch := fixture.manager.residency.activations[grandchildID]
	owned := false
	if parentEpoch := fixture.manager.residency.activations[childID]; parentEpoch != nil {
		_, owned = parentEpoch.ownedChildren[grandchildID]
	}
	fixture.manager.residency.mutex.Unlock()
	if epoch != nil {
		t.Fatal("externally disposed Activation remained addressable")
	}
	if !owned {
		t.Fatal("ownership was released before terminal lifecycle publication")
	}
	select {
	case <-disposed:
		t.Fatal("external disposal completed before terminal publication returned")
	default:
	}
	close(releaseTerminal)
	select {
	case <-disposed:
	case <-time.After(time.Second):
		t.Fatal("external disposal did not finish")
	}
	fixture.manager.residency.mutex.Lock()
	parentEpoch := fixture.manager.residency.activations[childID]
	_, owned = parentEpoch.ownedChildren[grandchildID]
	fixture.manager.residency.mutex.Unlock()
	if owned {
		t.Fatal("ownership remained after terminal lifecycle publication")
	}
}
