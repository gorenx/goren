package subagent_test

import (
	"context"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

func TestBoundChildExecutionStopsWhenParentCloses(
	testingContext *testing.T,
) {
	childGate := make(chan struct{})
	state := newIntegrationFixture(
		testingContext,
		[][]llm.StreamChunk{
			continuableTextResponse("parent answer before close"),
			continuableTextResponse("unreachable child answer"),
		},
	)
	state.backend.setGates([]<-chan struct{}{
		nil,
		childGate,
	})
	createBoundDefinition(
		testingContext,
		state,
		boundcontract.Draft{
			Name:         "researcher",
			Enabled:      true,
			SystemPrompt: "Stop with the parent.",
		},
	)
	parentHandle := state.createParent(testingContext)
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		3*time.Second,
	)
	defer cancelWait()
	bindingValue := waitForBoundMaterialization(
		testingContext,
		waitContext,
		state,
		parentHandle.Subject,
		"researcher",
	)
	if err := parentHandle.Subject.Followup(
		integrationUserMessage(testingContext, "start gated background work"),
	); err != nil {
		testingContext.Fatal(err)
	}
	if err := state.backend.waitForRequests(waitContext, 2); err != nil {
		testingContext.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		closed <- parentHandle.Dispose(context.Background())
	}()
	select {
	case err := <-closed:
		if err != nil {
			testingContext.Fatal(err)
		}
	case <-waitContext.Done():
		testingContext.Fatal("parent close did not cancel Bound child execution")
	}
	for _, identifier := range []session.SessionID{
		parentHandle.Subject.ID(),
		bindingValue.ChildSessionID,
	} {
		if _, found := state.agents.Get(identifier); found {
			testingContext.Fatalf("Agent %q remained live after parent close", identifier)
		}
		if _, found := state.sessions.Get(identifier); found {
			testingContext.Fatalf("Session %q remained live after parent close", identifier)
		}
	}
	assertNoIntegrationFailures(testingContext, state)
}
