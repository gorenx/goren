package subagent_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	boundcontract "github.com/gorenx/goren/subagent/bound"
	"github.com/gorenx/goren/subagent/bound/turnrelay"
)

type integrationCursorFlushFailure struct {
	plugin.Base
	mutex    sync.Mutex
	failure  error
	attempts int
	changed  chan struct{}
}

func (*integrationCursorFlushFailure) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "bound-integration-cursor-flush-failure",
		Events: []plugin.EventSubscription{
			plugin.EventOf[session.FlushRequested](),
		},
	}
}

func (*integrationCursorFlushFailure) Apply(context.Context) error { return nil }

func (*integrationCursorFlushFailure) Dispose(context.Context) error { return nil }

func (observer *integrationCursorFlushFailure) ObserveEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	requested, matches := fact.(session.FlushRequested)
	if !matches || requested.Conversation == nil ||
		requested.Conversation.ID() != "integration-parent" ||
		!hasCommittedProgressAfterTurnEnd(requested.Conversation.Events()) {
		return nil
	}
	observer.mutex.Lock()
	observer.attempts++
	attempt := observer.attempts
	observer.mutex.Unlock()
	select {
	case observer.changed <- struct{}{}:
	default:
	}
	if attempt == 1 {
		return observer.failure
	}
	return nil
}

func hasCommittedProgressAfterTurnEnd(events []session.Event) bool {
	lastTurnEnd := int64(-1)
	for _, committed := range events {
		if committed.Type == session.TurnEndEventName {
			lastTurnEnd = committed.Seq
		}
	}
	return lastTurnEnd >= 0 && events[len(events)-1].Seq > lastTurnEnd
}

func (observer *integrationCursorFlushFailure) waitForAttempts(
	waitContext context.Context,
	want int,
) error {
	for {
		observer.mutex.Lock()
		attempts := observer.attempts
		observer.mutex.Unlock()
		if attempts >= want {
			return nil
		}
		select {
		case <-waitContext.Done():
			return context.Cause(waitContext)
		case <-observer.changed:
		}
	}
}

func TestBoundDisabledTurnIsDeliveredAfterDefinitionIsEnabled(
	testingContext *testing.T,
) {
	state := newIntegrationFixture(
		testingContext,
		[][]llm.StreamChunk{
			continuableTextResponse("parent answered while Bound was disabled"),
			continuableTextResponse("Bound processed the pending turn"),
		},
	)
	created := createBoundDefinition(
		testingContext,
		state,
		boundcontract.Draft{
			Name:         "researcher",
			Enabled:      true,
			SystemPrompt: "revision one",
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
	disabled := replaceBoundDefinition(
		testingContext,
		state,
		created.Revision,
		boundcontract.Draft{
			Name:         "researcher",
			Enabled:      false,
			SystemPrompt: "revision two",
		},
	)
	waitForIntegrationAgentAbsent(
		testingContext,
		waitContext,
		state,
		bindingValue.ChildSessionID,
	)
	parentMessage := integrationUserMessage(
		testingContext,
		"queue this while the Bound is disabled",
	)
	if err := parentHandle.Subject.Followup(parentMessage); err != nil {
		testingContext.Fatal(err)
	}
	if err := parentHandle.Subject.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
	if err := state.backend.waitForRequests(waitContext, 1); err != nil {
		testingContext.Fatal(err)
	}
	assertIntegrationRequestCountFor(
		testingContext,
		state.backend,
		1,
		600*time.Millisecond,
	)
	replaceBoundDefinition(
		testingContext,
		state,
		disabled.Revision,
		boundcontract.Draft{
			Name:         "researcher",
			Enabled:      true,
			SystemPrompt: "revision three",
		},
	)
	childAgent := waitForIntegrationAgent(
		testingContext,
		waitContext,
		state,
		bindingValue.ChildSessionID,
	)
	if err := state.backend.waitForRequests(waitContext, 2); err != nil {
		testingContext.Fatal(err)
	}
	if err := childAgent.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
	assertBoundChildIdentity(
		testingContext,
		childAgent,
		parentHandle.Subject.ID(),
		3,
	)
	assertSingleBoundDelivery(
		testingContext,
		childAgent.SessionValue(),
		parentHandle.Subject.ID(),
	)
	requests := state.backend.snapshots()
	if len(requests) != 2 ||
		requests[1].SessionID != string(bindingValue.ChildSessionID) ||
		!strings.Contains(
			lastUserContentText(requests[1].Messages),
			"queue this while the Bound is disabled",
		) {
		testingContext.Fatalf("resumed Bound requests = %#v", requests)
	}
	assertNoIntegrationFailures(testingContext, state)
}

func TestBoundTurnRelayCatchesUpAfterPluginReload(
	testingContext *testing.T,
) {
	state := newIntegrationFixture(
		testingContext,
		[][]llm.StreamChunk{
			continuableTextResponse("parent first answer"),
			continuableTextResponse("Bound first answer"),
			continuableTextResponse("parent second answer"),
			continuableTextResponse("Bound recovered answer"),
		},
	)
	createBoundDefinition(
		testingContext,
		state,
		boundcontract.Draft{
			Name:         "researcher",
			Enabled:      true,
			SystemPrompt: "Recover missed parent turns.",
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
	childAgent := waitForIntegrationAgent(
		testingContext,
		waitContext,
		state,
		bindingValue.ChildSessionID,
	)
	if err := parentHandle.Subject.Followup(
		integrationUserMessage(testingContext, "first direct turn"),
	); err != nil {
		testingContext.Fatal(err)
	}
	if err := parentHandle.Subject.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
	if err := state.backend.waitForRequests(waitContext, 2); err != nil {
		testingContext.Fatal(err)
	}
	if err := childAgent.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
	waitForTurnRelayProgress(
		testingContext,
		waitContext,
		parentHandle.Subject.SessionValue(),
		1,
	)
	if err := state.runtimeEngine.Unload(
		context.Background(),
		state.turnRelayHandle,
	); err != nil {
		testingContext.Fatal(err)
	}
	if err := parentHandle.Subject.Followup(
		integrationUserMessage(testingContext, "turn committed without relay"),
	); err != nil {
		testingContext.Fatal(err)
	}
	if err := parentHandle.Subject.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
	if err := state.backend.waitForRequests(waitContext, 3); err != nil {
		testingContext.Fatal(err)
	}
	if len(state.backend.snapshots()) != 3 {
		testingContext.Fatal("unloaded Turn Relay delivered the second turn")
	}
	if _, err := state.runtimeEngine.Mount(
		context.Background(),
		turnrelay.New(turnrelay.Diagnostics{
			WorkerError: state.observerErrors.report,
		}),
	); err != nil {
		testingContext.Fatal(err)
	}
	if err := state.backend.waitForRequests(waitContext, 4); err != nil {
		testingContext.Fatal(err)
	}
	if err := childAgent.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
	waitForTurnRelayProgress(
		testingContext,
		waitContext,
		parentHandle.Subject.SessionValue(),
		2,
	)
	assertBoundDeliveryCount(
		testingContext,
		childAgent.SessionValue(),
		parentHandle.Subject.ID(),
		2,
	)
	requests := state.backend.snapshots()
	if requests[3].SessionID != string(bindingValue.ChildSessionID) ||
		!strings.Contains(
			lastUserContentText(requests[3].Messages),
			"turn committed without relay",
		) {
		testingContext.Fatalf("recovered Bound request = %#v", requests[3])
	}
	assertNoIntegrationFailures(testingContext, state)
}

func TestBoundRelayReusesReceiptWhenCursorFlushRetries(
	testingContext *testing.T,
) {
	sentinel := errors.New("test: parent cursor flush failed")
	flushFailure := &integrationCursorFlushFailure{
		failure: sentinel,
		changed: make(chan struct{}, 1),
	}
	state := newIntegrationFixture(
		testingContext,
		[][]llm.StreamChunk{
			continuableTextResponse("parent answer before cursor failure"),
			continuableTextResponse("Bound accepted once"),
		},
		flushFailure,
	)
	createBoundDefinition(
		testingContext,
		state,
		boundcontract.Draft{
			Name:         "researcher",
			Enabled:      true,
			SystemPrompt: "Retry cursor persistence safely.",
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
	childAgent := waitForIntegrationAgent(
		testingContext,
		waitContext,
		state,
		bindingValue.ChildSessionID,
	)
	if err := parentHandle.Subject.Followup(
		integrationUserMessage(testingContext, "exercise the cursor retry window"),
	); err != nil {
		testingContext.Fatal(err)
	}
	if err := state.backend.waitForRequests(waitContext, 2); err != nil {
		testingContext.Fatal(err)
	}
	if err := flushFailure.waitForAttempts(waitContext, 2); err != nil {
		testingContext.Fatal(err)
	}
	if err := childAgent.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
	assertIntegrationRequestCountFor(
		testingContext,
		state.backend,
		2,
		300*time.Millisecond,
	)
	assertBoundDeliveryCount(
		testingContext,
		childAgent.SessionValue(),
		parentHandle.Subject.ID(),
		1,
	)
	if failures := state.eventFailures.snapshot(); len(failures) != 0 {
		testingContext.Fatalf("event observer failures = %#v", failures)
	}
	failures := state.observerErrors.snapshot()
	if len(failures) != 1 || !errors.Is(failures[0], sentinel) {
		testingContext.Fatalf("Turn Relay retry diagnostics = %#v", failures)
	}
}
