package continuation

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestStartRollsBackPublishedMaterializationAtScopedDrainCutoff(t *testing.T) {
	fixture := newManagerFixture(t)
	drainResult := drainWhenPublished(t, fixture)
	childID := session.SessionID("drained-start")

	_, startErr := fixture.manager.Start(
		context.Background(),
		subagent.ContinuableStartSpec{
			Provider: "spawn",
			Label:    "review",
			ChildID:  &childID,
			Request: subagent.ContinuableRequest{
				Parent: fixture.parent,
				Prompt: []llm.ContentBlock{
					llm.NewTextBlock("must not be accepted"),
				},
			},
		},
	)
	assertDraining(t, startErr)
	assertDrainCompleted(t, drainResult)
	assertMaterializationRolledBack(t, fixture, childID)
}

func TestColdFollowupRollsBackPublishedMaterializationAtScopedDrainCutoff(t *testing.T) {
	fixture := newManagerFixture(t)
	childID := session.SessionID("drained-resume")
	fixture.storeContinuableChild(t, childID)
	drainResult := drainWhenPublished(t, fixture)

	_, followupErr := fixture.manager.Followup(
		context.Background(),
		fixture.parent,
		childID,
		[]llm.ContentBlock{
			llm.NewTextBlock("must not be accepted"),
		},
		subagent.FollowupOptions{
			Source: subagent.CoordinatorSource{
				SenderSessionID: fixture.parent.ID(),
			},
		},
	)
	assertDraining(t, followupErr)
	assertDrainCompleted(t, drainResult)
	assertMaterializationRolledBack(t, fixture, childID)
}

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

func drainWhenPublished(
	t *testing.T,
	fixture *managerFixture,
) <-chan error {
	t.Helper()
	drainResult := make(chan error, 1)
	fixture.lifecycle.startedHook = func(agent.Agent, subagent.Started) {
		go func() {
			drainResult <- fixture.manager.DrainDescendants(
				context.Background(),
				[]agent.Agent{fixture.parent},
			)
		}()
		deadline := time.Now().Add(time.Second)
		for {
			fixture.manager.residency.mutex.Lock()
			closing := agent.Same(
				fixture.manager.residency.closingRoots[fixture.parent.ID()],
				fixture.parent,
			)
			fixture.manager.residency.mutex.Unlock()
			if closing {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("scoped drain did not establish its admission cutoff")
			}
			runtime.Gosched()
		}
	}
	return drainResult
}

func assertDraining(t *testing.T, problem error) {
	t.Helper()
	var subagentProblem *subagent.Error
	if !errors.As(problem, &subagentProblem) ||
		subagentProblem.Code != subagent.ErrorDraining {
		t.Fatalf("materialization at drain cutoff = %v", problem)
	}
}

func assertDrainCompleted(t *testing.T, drainResult <-chan error) {
	t.Helper()
	select {
	case drainErr := <-drainResult:
		if drainErr != nil {
			t.Fatal(drainErr)
		}
	case <-time.After(time.Second):
		t.Fatal("scoped drain did not wait for materialization rollback")
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
