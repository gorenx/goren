package subagent_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

func TestBoundBindingDoesNotWaitForParentMaintenance(
	testingContext *testing.T,
) {
	state := newIntegrationFixture(testingContext, nil)
	parentHandle := state.createParent(testingContext)
	maintenanceEntered := make(chan struct{})
	releaseMaintenance := make(chan struct{})
	var releaseOnce sync.Once
	testingContext.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseMaintenance) })
	})
	maintenanceDone := make(chan error, 1)
	go func() {
		maintenanceDone <- parentHandle.Subject.RunMaintenance(
			context.Background(),
			func(maintenanceContext context.Context) error {
				close(maintenanceEntered)
				select {
				case <-maintenanceContext.Done():
					return context.Cause(maintenanceContext)
				case <-releaseMaintenance:
					return nil
				}
			},
		)
	}()
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		3*time.Second,
	)
	defer cancelWait()
	select {
	case <-maintenanceEntered:
	case <-waitContext.Done():
		testingContext.Fatal("parent maintenance did not start")
	}
	createBoundDefinition(
		testingContext,
		state,
		boundcontract.Draft{
			Name:         "researcher",
			Enabled:      true,
			SystemPrompt: "Bind without reserving parent maintenance.",
		},
	)
	bindingValue := waitForBoundMaterialization(
		testingContext,
		waitContext,
		state,
		parentHandle.Subject,
		"researcher",
	)
	childAgent := waitForIntegrationAgent(
		testingContext,
		waitContext,
		state,
		bindingValue.ChildSessionID,
	)
	assertBoundChildIdentity(
		testingContext,
		childAgent,
		parentHandle.Subject.ID(),
		1,
	)
	if requests := state.backend.snapshots(); len(requests) != 0 {
		testingContext.Fatalf(
			"Bound binding during maintenance issued %d model requests",
			len(requests),
		)
	}
	releaseOnce.Do(func() { close(releaseMaintenance) })
	select {
	case err := <-maintenanceDone:
		if err != nil {
			testingContext.Fatal(err)
		}
	case <-waitContext.Done():
		testingContext.Fatal("parent maintenance did not finish")
	}
	assertNoIntegrationFailures(testingContext, state)
}

func TestBoundReplacementSerializesRunningEpochAndNextInteraction(
	testingContext *testing.T,
) {
	oldEpochGate := make(chan struct{})
	var releaseOnce sync.Once
	testingContext.Cleanup(func() {
		releaseOnce.Do(func() { close(oldEpochGate) })
	})
	state := newIntegrationFixture(
		testingContext,
		[][]llm.StreamChunk{
			continuableTextResponse("parent revision one answer"),
			continuableTextResponse("old epoch answer"),
			continuableTextResponse("parent revision two answer"),
			continuableTextResponse("new epoch answer"),
		},
	)
	state.backend.setGates([]<-chan struct{}{
		nil,
		oldEpochGate,
	})
	created := createBoundDefinition(
		testingContext,
		state,
		boundcontract.Draft{
			Name:         "researcher",
			Enabled:      true,
			SystemPrompt: "revision one prompt",
		},
	)
	parentHandle := state.createParent(testingContext)
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		4*time.Second,
	)
	defer cancelWait()
	bindingValue := waitForBoundMaterialization(
		testingContext,
		waitContext,
		state,
		parentHandle.Subject,
		"researcher",
	)
	oldEpoch := waitForIntegrationAgent(
		testingContext,
		waitContext,
		state,
		bindingValue.ChildSessionID,
	)
	if err := parentHandle.Subject.Followup(
		integrationUserMessage(testingContext, "first interaction"),
	); err != nil {
		testingContext.Fatal(err)
	}
	if err := state.backend.waitForRequests(waitContext, 2); err != nil {
		testingContext.Fatal(err)
	}
	replaceBoundDefinition(
		testingContext,
		state,
		created.Revision,
		boundcontract.Draft{
			Name:         "researcher",
			Enabled:      true,
			SystemPrompt: "revision two prompt",
		},
	)
	if err := parentHandle.Subject.Followup(
		integrationUserMessage(testingContext, "second interaction"),
	); err != nil {
		testingContext.Fatal(err)
	}
	if err := state.backend.waitForRequests(waitContext, 3); err != nil {
		testingContext.Fatal(err)
	}
	if requests := state.backend.snapshots(); len(requests) != 3 {
		testingContext.Fatalf(
			"new Bound epoch ran before old epoch settled: requests=%d",
			len(requests),
		)
	}
	releaseOnce.Do(func() { close(oldEpochGate) })
	if err := state.backend.waitForRequests(waitContext, 4); err != nil {
		testingContext.Fatal(err)
	}
	if err := parentHandle.Subject.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
	newEpoch := waitForBoundAppliedRevision(
		testingContext,
		waitContext,
		state,
		bindingValue.ChildSessionID,
		oldEpoch,
		2,
	)
	if err := newEpoch.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
	waitForTurnRelayProgress(
		testingContext,
		waitContext,
		parentHandle.Subject.SessionValue(),
		2,
	)
	requests := state.backend.snapshots()
	if len(requests) != 4 ||
		requests[1].SessionID != string(bindingValue.ChildSessionID) ||
		requests[3].SessionID != string(bindingValue.ChildSessionID) ||
		requests[1].System == nil || requests[3].System == nil ||
		!strings.Contains(*requests[1].System, "revision one prompt") ||
		!strings.Contains(*requests[3].System, "revision two prompt") ||
		!strings.Contains(
			lastUserContentText(requests[3].Messages),
			"second interaction",
		) {
		testingContext.Fatalf("replacement requests = %#v", requests)
	}
	if newEpoch.ID() != oldEpoch.ID() || agent.Same(newEpoch, oldEpoch) {
		testingContext.Fatal("replacement did not reuse child identity with a new epoch")
	}
	assertBoundDeliveryCount(
		testingContext,
		newEpoch.SessionValue(),
		parentHandle.Subject.ID(),
		2,
	)
	assertNoIntegrationFailures(testingContext, state)
}

func TestRuntimeShutdownCancelsRunningBoundChild(
	testingContext *testing.T,
) {
	childGate := make(chan struct{})
	state := newIntegrationFixture(
		testingContext,
		[][]llm.StreamChunk{
			continuableTextResponse("parent answer before shutdown"),
			continuableTextResponse("unreachable Bound answer"),
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
			SystemPrompt: "Stop during Runtime shutdown.",
		},
	)
	parentHandle := state.createParent(testingContext)
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		4*time.Second,
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
		integrationUserMessage(testingContext, "start work before shutdown"),
	); err != nil {
		testingContext.Fatal(err)
	}
	if err := state.backend.waitForRequests(waitContext, 2); err != nil {
		testingContext.Fatal(err)
	}
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- state.runtimeEngine.Shutdown(context.Background())
	}()
	select {
	case err := <-shutdownDone:
		if err != nil {
			testingContext.Fatal(err)
		}
	case <-waitContext.Done():
		testingContext.Fatal("Runtime shutdown did not cancel running Bound child")
	}
	for _, identifier := range []session.SessionID{
		parentHandle.Subject.ID(),
		bindingValue.ChildSessionID,
	} {
		if _, found := state.agents.Get(identifier); found {
			testingContext.Fatalf("Agent %q remained after Runtime shutdown", identifier)
		}
	}
	if sessions := state.sessions.List(); len(sessions) != 0 {
		testingContext.Fatalf("live Sessions after Runtime shutdown = %#v", sessions)
	}
}
