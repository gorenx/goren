package subagent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	boundcontract "github.com/gorenx/goren/subagent/bound"
	"github.com/gorenx/goren/subagent/tools/report"
)

func TestBoundRelaysCompletedParentTurnToChild(testingContext *testing.T) {
	const boundPrompt = "Research parent interactions in the background."
	state := newIntegrationFixture(
		testingContext,
		[][]llm.StreamChunk{
			{
				llm.BlockEndChunk{
					Index: 0,
					Block: agentmessage.ReasoningBlock{
						Text: "hidden parent reasoning",
					},
				},
				llm.BlockEndChunk{
					Index: 1,
					Block: agentmessage.NewTextBlock(
						"visible parent answer",
					),
				},
				llm.FinishChunk{
					Reason: llm.StopFinish{},
				},
			},
			continuableTextResponse("bound child processed the interaction"),
		},
	)
	createBoundDefinition(
		testingContext,
		state,
		boundcontract.Draft{
			Name:         "researcher",
			Enabled:      true,
			SystemPrompt: boundPrompt,
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
	assertBoundChildIdentity(
		testingContext,
		childAgent,
		parentHandle.Subject.ID(),
		1,
	)
	if requests := state.backend.snapshots(); len(requests) != 0 {
		testingContext.Fatalf(
			"Bound materialization issued %d model requests, want 0",
			len(requests),
		)
	}
	parentMessage, messageErr := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock("investigate the release risk"),
			},
			Source: agentmessage.UserMessageSource{},
		},
	)
	if messageErr != nil {
		testingContext.Fatal(messageErr)
	}
	if followupErr := parentHandle.Subject.Followup(parentMessage); followupErr != nil {
		testingContext.Fatal(followupErr)
	}
	if idleErr := parentHandle.Subject.WhenIdle(waitContext); idleErr != nil {
		testingContext.Fatal(idleErr)
	}
	if requestErr := state.backend.waitForRequests(waitContext, 2); requestErr != nil {
		testingContext.Fatal(requestErr)
	}
	if idleErr := childAgent.WhenIdle(waitContext); idleErr != nil {
		testingContext.Fatal(idleErr)
	}
	requests := state.backend.snapshots()
	if len(requests) != 2 {
		testingContext.Fatalf(
			"model request count = %d, want parent + Bound child",
			len(requests),
		)
	}
	if requests[0].SessionID != string(parentHandle.Subject.ID()) ||
		requests[1].SessionID != string(bindingValue.ChildSessionID) {
		testingContext.Fatalf(
			"model request Sessions = %q, %q",
			requests[0].SessionID,
			requests[1].SessionID,
		)
	}
	if requests[1].System == nil ||
		!strings.Contains(*requests[1].System, boundPrompt) {
		testingContext.Fatalf(
			"Bound system prompt = %#v",
			requests[1].System,
		)
	}
	childInput := lastUserContentText(requests[1].Messages)
	for _, want := range []string{
		"User:",
		"investigate the release risk",
		"Parent:",
		"visible parent answer",
	} {
		if !strings.Contains(childInput, want) {
			testingContext.Fatalf(
				"Bound child input %q does not contain %q",
				childInput,
				want,
			)
		}
	}
	if strings.Contains(childInput, "hidden parent reasoning") {
		testingContext.Fatalf(
			"Bound child input exposed parent reasoning: %q",
			childInput,
		)
	}
	assertSingleBoundDelivery(
		testingContext,
		childAgent.SessionValue(),
		parentHandle.Subject.ID(),
	)
	assertNoIntegrationFailures(testingContext, state)
}

func TestMultipleBoundsReceiveParentTurnIndependently(
	testingContext *testing.T,
) {
	state := newIntegrationFixture(
		testingContext,
		[][]llm.StreamChunk{
			continuableTextResponse("parent visible answer"),
			continuableTextResponse("first Bound answer"),
			continuableTextResponse("second Bound answer"),
		},
	)
	for _, definitionValue := range []boundcontract.Draft{
		{
			Name:         "first",
			Enabled:      true,
			SystemPrompt: "First background role.",
		},
		{
			Name:         "second",
			Enabled:      true,
			SystemPrompt: "Second background role.",
		},
	} {
		createBoundDefinition(testingContext, state, definitionValue)
	}
	parentHandle := state.createParent(testingContext)
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		3*time.Second,
	)
	defer cancelWait()
	firstBinding := waitForBoundMaterialization(
		testingContext,
		waitContext,
		state,
		parentHandle.Subject,
		"first",
	)
	secondBinding := waitForBoundMaterialization(
		testingContext,
		waitContext,
		state,
		parentHandle.Subject,
		"second",
	)
	if err := parentHandle.Subject.Followup(
		integrationUserMessage(testingContext, "notify every background role"),
	); err != nil {
		testingContext.Fatal(err)
	}
	if err := parentHandle.Subject.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
	if err := state.backend.waitForRequests(waitContext, 3); err != nil {
		testingContext.Fatal(err)
	}
	firstChild := waitForIntegrationAgent(
		testingContext,
		waitContext,
		state,
		firstBinding.ChildSessionID,
	)
	secondChild := waitForIntegrationAgent(
		testingContext,
		waitContext,
		state,
		secondBinding.ChildSessionID,
	)
	if err := firstChild.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
	if err := secondChild.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
	requests := state.backend.snapshots()
	// Key is a Bound child Session ID. Value records whether its model request
	// contained the relayed direct-user interaction.
	childRequests := make(map[session.SessionID]bool, 2)
	for _, requestValue := range requests[1:] {
		requestSessionID := session.SessionID(requestValue.SessionID)
		if requestSessionID != firstBinding.ChildSessionID &&
			requestSessionID != secondBinding.ChildSessionID {
			testingContext.Fatalf(
				"unexpected Bound request Session %q",
				requestValue.SessionID,
			)
		}
		childRequests[requestSessionID] = strings.Contains(
			lastUserContentText(requestValue.Messages),
			"notify every background role",
		)
	}
	if len(childRequests) != 2 ||
		!childRequests[firstBinding.ChildSessionID] ||
		!childRequests[secondBinding.ChildSessionID] {
		testingContext.Fatalf("Bound child requests = %#v", childRequests)
	}
	assertSingleBoundDelivery(
		testingContext,
		firstChild.SessionValue(),
		parentHandle.Subject.ID(),
	)
	assertSingleBoundDelivery(
		testingContext,
		secondChild.SessionValue(),
		parentHandle.Subject.ID(),
	)
	assertNoIntegrationFailures(testingContext, state)
}

func TestBoundReportOnlyParentTurnDoesNotEchoToChild(
	testingContext *testing.T,
) {
	reportPlugin, err := report.New(report.NextStep)
	if err != nil {
		testingContext.Fatal(err)
	}
	state := newIntegrationFixture(
		testingContext,
		[][]llm.StreamChunk{
			continuableTextResponse("parent visible answer"),
			{
				llm.BlockEndChunk{
					Index: 0,
					Block: agentmessage.ToolCallBlock{
						ID:        "bound-report-call",
						Name:      "report",
						Arguments: `{"output":"Bound selected finding"}`,
					},
				},
				llm.FinishChunk{
					Reason: llm.ToolCallsFinish{},
				},
			},
			continuableTextResponse("parent acknowledged the Bound report"),
			continuableTextResponse("Bound finished after reporting"),
		},
		reportPlugin,
	)
	createBoundDefinition(
		testingContext,
		state,
		boundcontract.Draft{
			Name:         "researcher",
			Enabled:      true,
			SystemPrompt: "Report selected findings.",
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
	if err = parentHandle.Subject.Followup(
		integrationUserMessage(testingContext, "investigate and report"),
	); err != nil {
		testingContext.Fatal(err)
	}
	if err = state.backend.waitForRequests(waitContext, 4); err != nil {
		testingContext.Fatal(err)
	}
	if err = parentHandle.Subject.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
	if err = childAgent.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
	waitForTurnRelayProgress(
		testingContext,
		waitContext,
		parentHandle.Subject.SessionValue(),
		2,
	)
	assertIntegrationRequestCountFor(
		testingContext,
		state.backend,
		4,
		350*time.Millisecond,
	)
	assertBoundDeliveryCount(
		testingContext,
		childAgent.SessionValue(),
		parentHandle.Subject.ID(),
		1,
	)
	parentMessages, deriveErr := parentHandle.Subject.SessionValue().DeriveMessages()
	if deriveErr != nil {
		testingContext.Fatal(deriveErr)
	}
	if !hasReportSource(
		parentMessages,
		"Bound selected finding",
		bindingValue.ChildSessionID,
	) {
		testingContext.Fatal("parent Session did not retain the Bound report")
	}
	assertNoIntegrationFailures(testingContext, state)
}
