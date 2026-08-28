package subagent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

func createBoundDefinition(
	testingContext *testing.T,
	state *integrationFixture,
	draftValue boundcontract.Draft,
) boundcontract.Definition {
	testingContext.Helper()
	if state.boundDefinitions == nil {
		testingContext.Fatal("Bound Definitions capability is unavailable")
	}
	created, err := state.boundDefinitions.Create(
		context.Background(),
		boundcontract.Creation{
			Definition: draftValue,
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return created
}

func replaceBoundDefinition(
	testingContext *testing.T,
	state *integrationFixture,
	expectedRevision int64,
	draftValue boundcontract.Draft,
) boundcontract.Definition {
	testingContext.Helper()
	replaced, err := state.boundDefinitions.Replace(
		context.Background(),
		boundcontract.Replacement{
			ExpectedRevision: expectedRevision,
			Definition:       draftValue,
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return replaced
}

func integrationUserMessage(
	testingContext *testing.T,
	textValue string,
) agentmessage.UserMessage {
	testingContext.Helper()
	messageValue, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock(textValue),
			},
			Source: agentmessage.UserMessageSource{},
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return messageValue
}

func assertIntegrationRequestCountFor(
	testingContext *testing.T,
	backend *integrationAdapter,
	want int,
	duration time.Duration,
) {
	testingContext.Helper()
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	changed := time.NewTicker(time.Millisecond)
	defer changed.Stop()
	for {
		if observed := len(backend.snapshots()); observed != want {
			testingContext.Fatalf(
				"model request count = %d during disabled interval, want %d",
				observed,
				want,
			)
		}
		select {
		case <-deadline.C:
			return
		case <-changed.C:
		}
	}
}

func waitForBoundMaterialization(
	testingContext *testing.T,
	waitContext context.Context,
	state *integrationFixture,
	parentAgent agent.Agent,
	boundName string,
) boundcontract.BindingData {
	testingContext.Helper()
	changed := time.NewTicker(time.Millisecond)
	defer changed.Stop()
	for {
		bindingValue, boundFound := integrationBinding(
			testingContext,
			parentAgent.SessionValue().Events(),
			boundName,
		)
		materialized := boundFound && integrationMaterialized(
			testingContext,
			parentAgent.SessionValue().Events(),
			bindingValue,
		)
		if materialized {
			if _, childFound := state.agents.Get(
				bindingValue.ChildSessionID,
			); childFound {
				return bindingValue
			}
		}
		select {
		case <-waitContext.Done():
			testingContext.Fatalf(
				"Bound %q did not materialize: %v; events=%#v; agents=%d; event failures=%#v; observer failures=%v",
				boundName,
				context.Cause(waitContext),
				parentAgent.SessionValue().Events(),
				len(state.agents.List()),
				state.eventFailures.snapshot(),
				state.observerErrors.snapshot(),
			)
		case <-changed.C:
		}
	}
}

func integrationBinding(
	testingContext *testing.T,
	events []session.Event,
	boundName string,
) (boundcontract.BindingData, bool) {
	testingContext.Helper()
	for _, committed := range events {
		if committed.Type != boundcontract.BindingEventName {
			continue
		}
		var bindingValue boundcontract.BindingData
		if err := json.Unmarshal(committed.Data, &bindingValue); err != nil {
			testingContext.Fatal(err)
		}
		if bindingValue.Name == boundName {
			return bindingValue, true
		}
	}
	return boundcontract.BindingData{}, false
}

func integrationMaterialized(
	testingContext *testing.T,
	events []session.Event,
	bindingValue boundcontract.BindingData,
) bool {
	testingContext.Helper()
	for _, committed := range events {
		if committed.Type != boundcontract.MaterializationEventName {
			continue
		}
		var materialization boundcontract.MaterializationData
		if err := json.Unmarshal(committed.Data, &materialization); err != nil {
			testingContext.Fatal(err)
		}
		if materialization.Name == bindingValue.Name &&
			materialization.ChildSessionID == bindingValue.ChildSessionID &&
			materialization.Result == boundcontract.MaterializationSucceeded {
			return true
		}
	}
	return false
}

func waitForIntegrationAgent(
	testingContext *testing.T,
	waitContext context.Context,
	state *integrationFixture,
	identifier session.SessionID,
) agent.Agent {
	testingContext.Helper()
	changed := time.NewTicker(time.Millisecond)
	defer changed.Stop()
	for {
		if subject, found := state.agents.Get(identifier); found {
			return subject
		}
		select {
		case <-waitContext.Done():
			testingContext.Fatalf(
				"Agent %q did not become live: %v",
				identifier,
				context.Cause(waitContext),
			)
		case <-changed.C:
		}
	}
}

func waitForIntegrationAgentAbsent(
	testingContext *testing.T,
	waitContext context.Context,
	state *integrationFixture,
	identifier session.SessionID,
) {
	testingContext.Helper()
	changed := time.NewTicker(time.Millisecond)
	defer changed.Stop()
	for {
		if _, found := state.agents.Get(identifier); !found {
			return
		}
		select {
		case <-waitContext.Done():
			testingContext.Fatalf(
				"Agent %q remained live: %v",
				identifier,
				context.Cause(waitContext),
			)
		case <-changed.C:
		}
	}
}

func assertBoundChildIdentity(
	testingContext *testing.T,
	childAgent agent.Agent,
	parentID session.SessionID,
	wantRevision int64,
) {
	testingContext.Helper()
	childHeader := childAgent.SessionValue().Header()
	if childHeader.ParentSession == nil ||
		*childHeader.ParentSession != parentID ||
		childHeader.Origin != session.OriginSubagent {
		testingContext.Fatalf("Bound child Header = %#v", childHeader)
	}
	for _, committed := range childAgent.SessionValue().Events() {
		if committed.Type != boundcontract.DefinitionAppliedEventName {
			continue
		}
		var applied boundcontract.DefinitionAppliedData
		if err := json.Unmarshal(committed.Data, &applied); err != nil {
			testingContext.Fatal(err)
		}
		if applied.Version == boundcontract.EventVersion &&
			applied.Definition.Revision == wantRevision {
			return
		}
	}
	testingContext.Fatalf(
		"Bound child %q did not apply Definition revision %d",
		childAgent.ID(),
		wantRevision,
	)
}

func assertSingleBoundDelivery(
	testingContext *testing.T,
	childSession session.Context,
	parentID session.SessionID,
) {
	testingContext.Helper()
	assertBoundDeliveryCount(testingContext, childSession, parentID, 1)
}

func assertBoundDeliveryCount(
	testingContext *testing.T,
	childSession session.Context,
	parentID session.SessionID,
	want int,
) {
	testingContext.Helper()
	messages, err := childSession.DeriveMessages()
	if err != nil {
		testingContext.Fatal(err)
	}
	deliveries := 0
	for _, messageValue := range messages {
		origin := messageValue.SourceValue()
		if origin == nil || origin.SourceKind() != boundcontract.DeliveryKind {
			continue
		}
		deliveryValue, decodeErr := boundcontract.DecodeDelivery(origin)
		if decodeErr != nil {
			testingContext.Fatal(decodeErr)
		}
		if !strings.HasPrefix(
			string(deliveryValue.Input),
			"session:"+string(parentID)+":seq:",
		) {
			testingContext.Fatalf(
				"Bound delivery Input ID = %q",
				deliveryValue.Input,
			)
		}
		deliveries++
	}
	if deliveries != want {
		testingContext.Fatalf(
			"Bound child delivery count = %d, want %d",
			deliveries,
			want,
		)
	}
}

func waitForTurnRelayProgress(
	testingContext *testing.T,
	waitContext context.Context,
	conversation session.Context,
	throughTurn int64,
) {
	testingContext.Helper()
	changed := time.NewTicker(time.Millisecond)
	defer changed.Stop()
	for {
		turnEndSeq := int64(-1)
		for _, committed := range conversation.Events() {
			if committed.Type != session.TurnEndEventName {
				continue
			}
			var ended session.TurnEnd
			if err := json.Unmarshal(committed.Data, &ended); err != nil {
				testingContext.Fatal(err)
			}
			if ended.Turn == throughTurn {
				turnEndSeq = committed.Seq
			}
		}
		if turnEndSeq >= 0 {
			for _, committed := range conversation.Events() {
				if committed.Seq > turnEndSeq {
					return
				}
			}
		}
		select {
		case <-waitContext.Done():
			testingContext.Fatalf(
				"Turn Relay did not advance through parent turn %d: %v",
				throughTurn,
				context.Cause(waitContext),
			)
		case <-changed.C:
		}
	}
}

func assertNoIntegrationFailures(
	testingContext *testing.T,
	state *integrationFixture,
) {
	testingContext.Helper()
	if failures := state.eventFailures.snapshot(); len(failures) != 0 {
		testingContext.Fatalf("event observer failures = %#v", failures)
	}
	if failures := state.observerErrors.snapshot(); len(failures) != 0 {
		testingContext.Fatalf("contained Subagent failures = %#v", failures)
	}
}
