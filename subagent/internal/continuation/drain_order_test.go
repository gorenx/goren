package continuation

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestDrainDescendantsReleasesNestedActivationsChildFirst(t *testing.T) {
	fixture := newManagerFixture(t)
	childID := session.SessionID("child")
	startContinuableForDrain(t, fixture.manager, fixture.parent, childID)
	childAgent, found := fixture.agents.Get(childID)
	if !found {
		t.Fatal("child was not published")
	}
	grandchildID := session.SessionID("grandchild")
	startContinuableForDrain(t, fixture.manager, childAgent, grandchildID)

	if drainErr := fixture.manager.DrainDescendants(
		context.Background(),
		[]agent.Agent{fixture.parent},
	); drainErr != nil {
		t.Fatal(drainErr)
	}
	if _, found := fixture.agents.Get(childID); found {
		t.Fatal("drained child remained live")
	}
	if _, found := fixture.agents.Get(grandchildID); found {
		t.Fatal("drained grandchild remained live")
	}
	terminalFacts := fixture.lifecycle.endedSnapshot()
	if len(terminalFacts) != 2 ||
		terminalFacts[0].ID != grandchildID ||
		terminalFacts[1].ID != childID {
		t.Fatalf("terminal order = %#v", terminalFacts)
	}
}

func TestDrainSettlesEveryActivationAfterDisposedObserverWithdrawal(t *testing.T) {
	fixture := newManagerFixture(t)
	childID := session.SessionID("withdrawn-child")
	startContinuableForDrain(t, fixture.manager, fixture.parent, childID)
	childAgent, found := fixture.agents.Get(childID)
	if !found {
		t.Fatal("child was not published")
	}
	grandchildID := session.SessionID("withdrawn-grandchild")
	startContinuableForDrain(t, fixture.manager, childAgent, grandchildID)

	// Plugin Runtime withdraws event subscriptions before invoking Dispose.
	// Simulate that ordering so Drain cannot rely on AgentDisposed callbacks.
	fixture.agents.disposed = nil
	if drainErr := fixture.manager.Drain(context.Background()); drainErr != nil {
		t.Fatal(drainErr)
	}
	fixture.manager.activations.mutex.Lock()
	remaining := len(fixture.manager.activations.activations)
	fixture.manager.activations.mutex.Unlock()
	if remaining != 0 {
		t.Fatalf("Drain retained %d business Activation(s)", remaining)
	}
	terminalFacts := fixture.lifecycle.endedSnapshot()
	if len(terminalFacts) != 2 ||
		terminalFacts[0].ID != grandchildID ||
		terminalFacts[1].ID != childID {
		t.Fatalf("terminal order = %#v", terminalFacts)
	}
}

func startContinuableForDrain(
	t *testing.T,
	owner *Manager,
	parentAgent agent.Agent,
	childID session.SessionID,
) {
	t.Helper()
	_, startErr := owner.Start(
		context.Background(),
		subagent.ContinuableStartSpec{
			Provider: "spawn",
			Label:    string(childID),
			ChildID:  &childID,
			Request: subagent.ContinuableRequest{
				Parent: parentAgent,
				Prompt: []llm.ContentBlock{
					llm.NewTextBlock("hold"),
				},
			},
		},
	)
	if startErr != nil {
		t.Fatal(startErr)
	}
}
