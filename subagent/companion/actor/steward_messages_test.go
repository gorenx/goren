package companionactor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	sessionactor "github.com/gorenx/goren/session/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"github.com/gorenx/goren/subagent/tools/control"
	"github.com/gorenx/goren/subagent/tools/report"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func messagingFixture(t *testing.T) (*stewardship, *Messenger, View) {
	t.Helper()
	var messaging *Messenger
	fixture := newStewardship(t, func(current *stewardship) tool.Providers {
		var err error
		messaging, err = NewMessenger(current.system, current.authority)
		if err != nil {
			t.Fatal(err)
		}
		dispatch, err := control.NewMessageProvider(messaging)
		if err != nil {
			t.Fatal(err)
		}
		reporting, err := report.NewProvider(messaging, report.NextStep)
		if err != nil {
			t.Fatal(err)
		}
		current.input.Tools = &toolregistry.Catalog{
			Schemas: []llm.ToolSchema{control.MessageSchema()},
		}
		current.creator.catalog = &toolregistry.Catalog{
			Schemas: []llm.ToolSchema{report.Schema()},
		}
		return tool.Providers{
			"send_message": dispatch,
			"report":       reporting,
		}
	})
	if _, err := fixture.client.Create(t.Context(), Create{
		Name:    "reviewer",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	return fixture, messaging, awaitPhase(t, fixture.client, "reviewer", Ready)
}

func (fixture *stewardship) activity(t *testing.T, expected agentregistry.Creation) loopactor.Activity {
	t.Helper()
	ready := request[agentregistry.Ready](t, fixture.system, fixture.authority, agentregistry.FindAgent{
		AgentID: expected.AgentID,
	})
	if ready.Err != nil || ready.Creation != expected {
		t.Fatalf("Agent unavailable: %+v", ready)
	}
	result := request[loopactor.Activity](t, fixture.system, ready.Executor, loopactor.ReadActivity{
		CreationID: expected.CreationID,
	})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	return result
}

// hold keeps the real Inbox available without asking the config-only fixture to
// run a model. Cleanup clears pending input before releasing maintenance.
func (fixture *stewardship) hold(t *testing.T, expected agentregistry.Creation) *goakt.GrainIdentity {
	t.Helper()
	ready := request[agentregistry.Ready](t, fixture.system, fixture.authority, agentregistry.FindAgent{
		AgentID: expected.AgentID,
	})
	if ready.Err != nil {
		t.Fatal(ready.Err)
	}
	result := request[agentactor.MaintenanceAccepted](t, fixture.system, ready.Executor, agentactor.BeginMaintenance{
		CreationID: expected.CreationID,
		RequestID:  "held-work",
		Worker:     fixture.configuration,
	})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		current, err := ask[agentregistry.Ready](ctx, fixture.system, fixture.authority, agentregistry.FindAgent{
			AgentID: expected.AgentID,
		})
		if err != nil || current.Err != nil || current.Creation != expected {
			return
		}
		_, _ = ask[loopactor.Changed](ctx, fixture.system, current.Executor, loopactor.Cancel{
			CreationID: expected.CreationID,
		})
		_, _ = ask[agentactor.MaintenanceFinished](ctx, fixture.system, current.Executor, agentactor.FinishMaintenance{
			CreationID: expected.CreationID,
			RequestID:  "held-work",
		})
	})
	return ready.Executor
}

func (fixture *stewardship) invoke(t *testing.T, ctx context.Context, caller agentregistry.Creation, name string, arguments json.RawMessage) runtimeactor.CallResult {
	t.Helper()
	target, err := goakt.GrainOf[*runtimeactor.Runtime](t.Context(), fixture.system, runtimeactor.RuntimeName(string(caller.AgentID)),
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
	)
	if err != nil {
		t.Fatal(err)
	}
	identifier := agentmessage.CallID(uuid.NewString())
	prepared := request[runtimeactor.CallResult](t, fixture.system, target, runtimeactor.PrepareCall{
		CreationID: caller.CreationID,
		Context:    ctx,
		Input: tools.ToolExecutionInput{
			SessionID: caller.AgentID,
			CallID:    identifier,
			Name:      name,
			Arguments: arguments,
		},
	})
	defer func() {
		if result := request[runtimeactor.Completion](t, fixture.system, target, runtimeactor.ReleaseCall{
			CreationID: caller.CreationID,
			CallID:     identifier,
		}); result.Err != nil {
			t.Fatal(result.Err)
		}
	}()
	if prepared.Err != nil {
		return prepared
	}
	if prepared.Stage == tools.ScheduledDispatch {
		result := request[runtimeactor.CallResult](t, fixture.system, target, runtimeactor.DispatchCall{
			CreationID: caller.CreationID,
			CallID:     identifier,
		})
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	final := request[runtimeactor.CallResult](t, fixture.system, target, runtimeactor.FinalizeCall{
		CreationID: caller.CreationID,
		CallID:     identifier,
	})
	if final.Err != nil || final.Result == nil {
		t.Fatalf("tool did not finalize: %+v", final)
	}
	return final
}

func acceptedMessage(t *testing.T, outcome runtimeactor.CallResult) agentmessage.MessageID {
	t.Helper()
	if outcome.Err != nil {
		t.Fatal(outcome.Err)
	}
	encoded, ok := outcome.Result.SuccessValue()
	if !ok {
		detail, _ := outcome.Result.FailureDetail()
		t.Fatalf("tool failed: %+v", detail)
	}
	var result struct {
		MessageID agentmessage.MessageID `json:"messageId"`
	}
	if err := json.Unmarshal(encoded, &result); err != nil || result.MessageID == "" {
		t.Fatalf("missing message confirmation: %s %v", encoded, err)
	}
	return result.MessageID
}

func (fixture *stewardship) persistedMessage(t *testing.T, expected agentregistry.Creation, identifier agentmessage.MessageID) agentmessage.UserMessage {
	t.Helper()
	endpoint := request[sessionregistry.Endpoint](t, fixture.system, fixture.sessions, sessionregistry.ResolveSession{
		SessionID:  expected.AgentID,
		CreationID: expected.CreationID,
	})
	if endpoint.Err != nil {
		t.Fatal(endpoint.Err)
	}
	captured := request[sessionactor.View](t, fixture.system, endpoint.Target, sessionactor.Read{
		CreationID: expected.CreationID,
	})
	if captured.Err != nil {
		t.Fatal(captured.Err)
	}
	flushed := request[sessionactor.Flushed](t, fixture.system, endpoint.Target, sessionactor.Flush{
		CreationID: expected.CreationID,
		Barrier:    captured.Snapshot.Barrier,
	})
	if flushed.Err != nil {
		t.Fatal(flushed.Err)
	}
	stored, err := fixture.creator.storage.Load(t.Context(), expected.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range stored.Events {
		if event.Type != agent.InboxSplicedEventName {
			continue
		}
		var change agent.InboxSplice
		if err := json.Unmarshal(event.Data, &change); err != nil {
			t.Fatal(err)
		}
		for _, entry := range change.Inserted {
			if entry.StableID() == identifier {
				return entry
			}
		}
	}
	t.Fatal("accepted message missing from target Session log")
	return agentmessage.UserMessage{}
}

func TestSharedToolsDeliverCompanionTasksAndReportsThroughRealRuntime(t *testing.T) {
	fixture, _, child := messagingFixture(t)
	fixture.hold(t, fixture.input.Creation)
	fixture.hold(t, child.Creation)
	if len(fixture.activity(t, fixture.input.Creation).Pending.NextStep) != 0 {
		t.Fatal("Companion startup sent unsolicited parent input")
	}
	arguments, _ := json.Marshal(map[string]string{
		"subagent_id": string(child.Creation.AgentID),
		"message":     "review these changes",
	})
	// An upstream creation value cannot replace the ToolActor's actual binding.
	ctx := tools.WithAgentCreationID(t.Context(), "forged-upstream")
	identifier := acceptedMessage(t, fixture.invoke(t, ctx, fixture.input.Creation, "send_message", arguments))
	queued := fixture.activity(t, child.Creation).Pending.NextTurn
	if len(queued) != 1 || queued[0].StableID() != identifier {
		t.Fatal("dispatch did not enter the child's next turn")
	}
	message := fixture.persistedMessage(t, child.Creation, identifier)
	origin, err := json.Marshal(message.SourceValue())
	if err != nil || string(origin) != `{"kind":"coordinator","form":"relay","senderSessionId":"parent"}` {
		t.Fatalf("task provenance=%s error=%v", origin, err)
	}
	if len(fixture.activity(t, fixture.input.Creation).Pending.NextStep) != 0 {
		t.Fatal("task delivery automatically copied child input to parent")
	}
	identifier = acceptedMessage(t, fixture.invoke(t, ctx, child.Creation, "report", json.RawMessage(`{"output":"review complete"}`)))
	queued = fixture.activity(t, fixture.input.Creation).Pending.NextStep
	if len(queued) != 1 || queued[0].StableID() != identifier {
		t.Fatal("report did not enter parent's next step")
	}
	message = fixture.persistedMessage(t, fixture.input.Creation, identifier)
	var source struct {
		Kind            string `json:"kind"`
		Form            string `json:"form"`
		SenderSessionID string `json:"senderSessionId"`
	}
	origin, _ = json.Marshal(message.SourceValue())
	if err := json.Unmarshal(origin, &source); err != nil || source.Kind != "subagent-report" || source.Form != "relay" || source.SenderSessionID != string(child.Creation.AgentID) {
		t.Fatalf("report provenance=%s error=%v", origin, err)
	}
}

func TestCompanionToolErrorsPreserveInboxAndDrainingChildCanReport(t *testing.T) {
	fixture, messaging, child := messagingFixture(t)
	fixture.hold(t, fixture.input.Creation)
	executor := fixture.hold(t, child.Creation)
	for _, input := range []map[string]any{
		{"subagent_id": "unknown", "message": "bad recipient"},
		{"subagent_id": "parent", "message": "self"},
		{"subagent_id": child.Creation.AgentID, "message": "forged", "parentSessionID": "parent"},
		{"subagent_id": child.Creation.AgentID},
	} {
		arguments, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if !fixture.invoke(t, t.Context(), fixture.input.Creation, "send_message", arguments).Result.Failed() {
			t.Fatalf("invalid dispatch accepted: %s", arguments)
		}
	}
	for _, arguments := range []string{
		`{"output":"forged recipient","parentSessionID":"other"}`,
		`{"output":12}`,
		`{}`,
	} {
		if !fixture.invoke(t, t.Context(), child.Creation, "report", json.RawMessage(arguments)).Result.Failed() {
			t.Fatalf("invalid report accepted: %s", arguments)
		}
	}
	for _, lifetime := range []string{"", "stale"} {
		ctx := tools.WithAgentCreationID(t.Context(), lifetime)
		if _, err := messaging.Send(ctx, control.Message{
			ParentSessionID: fixture.input.Creation.AgentID,
			ChildSessionID:  child.Creation.AgentID,
			Content:         []agentmessage.ContentBlock{agentmessage.NewTextBlock("forged")},
		}); err == nil {
			t.Fatal("stale parent accepted")
		}
		if _, err := messaging.Report(ctx, report.Report{
			ChildSessionID: child.Creation.AgentID,
			Output:         "forged",
			Delivery:       report.Quiet,
		}); err == nil {
			t.Fatal("stale child accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result := fixture.invoke(t, ctx, child.Creation, "report", json.RawMessage(`{"output":"canceled"}`)); !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("canceled preparation: %+v", result)
	}
	if len(fixture.activity(t, child.Creation).Pending.NextTurn) != 0 || len(fixture.activity(t, fixture.input.Creation).Pending.NextStep) != 0 {
		t.Fatal("failed calls changed Inbox")
	}
	if _, err := fixture.client.Remove(t.Context(), Remove{
		Name:             "reviewer",
		ExpectedRevision: child.Companion.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, fixture.client, "reviewer", Draining)
	if _, err := fixture.client.SendMessage(t.Context(), child.Creation.AgentID, []agentmessage.ContentBlock{agentmessage.NewTextBlock("too late")}); err == nil {
		t.Fatal("draining child accepted new task")
	}
	acceptedMessage(t, fixture.invoke(t, t.Context(), child.Creation, "report", json.RawMessage(`{"output":"final in-flight report"}`)))
	if result := request[agentactor.MaintenanceFinished](t, fixture.system, executor, agentactor.FinishMaintenance{
		CreationID: child.Creation.CreationID,
		RequestID:  "held-work",
	}); result.Err != nil {
		t.Fatal(result.Err)
	}
	awaitPhase(t, fixture.client, "reviewer", Stopped)
	if _, err := messaging.Report(tools.WithAgentCreationID(t.Context(), child.Creation.CreationID), report.Report{
		ChildSessionID: child.Creation.AgentID,
		Output:         "after stop",
		Delivery:       report.Quiet,
	}); err == nil {
		t.Fatal("closed child reported")
	}
}

func TestCompanionQuietReportDoesNotWakeParentAndClosureRejectsDelivery(t *testing.T) {
	fixture, messaging, child := messagingFixture(t)
	ctx := tools.WithAgentCreationID(t.Context(), child.Creation.CreationID)
	identifier, err := messaging.Report(ctx, report.Report{
		ChildSessionID: child.Creation.AgentID,
		Output:         "quiet finding",
		Delivery:       report.Quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	current := fixture.activity(t, fixture.input.Creation)
	if current.Status != agent.StatusIdle || len(current.Pending.NextStep) != 1 || current.Pending.NextStep[0].StableID() != identifier {
		t.Fatalf("quiet report woke parent or lost input: %+v", current)
	}
	if err := fixture.client.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.client.Report(t.Context(), child.Creation, "late", report.Quiet); !errors.Is(err, agent.ErrClosing) {
		t.Fatalf("closed Steward accepted report: %v", err)
	}
	closed := request[agentactor.Closed](t, fixture.system, fixture.authority, agentregistry.CloseAgent{
		Creation: fixture.input.Creation,
	})
	if closed.Err != nil {
		t.Fatal(closed.Err)
	}
	stored, err := fixture.creator.storage.Load(t.Context(), fixture.input.Creation.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := agent.NewInbox(stored.Header, stored.Events)
	if err != nil || len(restored.NextStep()) != 1 || restored.NextStep()[0].StableID() != identifier {
		t.Fatalf("parent close lost committed next-step report: %v", err)
	}
}

func TestCompanionReportRejectsReplacedLifetimeAndOrdinaryParent(t *testing.T) {
	fixture, messaging, first := messagingFixture(t)
	if _, err := messaging.Report(tools.WithAgentCreationID(t.Context(), fixture.input.Creation.CreationID), report.Report{
		ChildSessionID: fixture.input.Creation.AgentID,
		Output:         "ordinary parent cannot report as a Companion",
		Delivery:       report.Quiet,
	}); !errors.Is(err, agent.ErrRole) {
		t.Fatalf("ordinary Agent admitted as reporting Companion: %v", err)
	}
	if _, err := fixture.client.Restart(t.Context(), Restart{
		Name:               first.Companion.Name,
		ExpectedRevision:   first.Companion.Revision,
		ExpectedCreationID: first.Creation.CreationID,
	}); err != nil {
		t.Fatal(err)
	}
	current := awaitPhase(t, fixture.client, first.Companion.Name, Ready)
	if current.Creation.CreationID == first.Creation.CreationID {
		t.Fatal("restart did not replace child lifetime")
	}
	if _, err := messaging.Report(tools.WithAgentCreationID(t.Context(), first.Creation.CreationID), report.Report{
		ChildSessionID: first.Creation.AgentID,
		Output:         "delayed old report",
		Delivery:       report.Quiet,
	}); !errors.Is(err, agentregistry.ErrCreation) {
		t.Fatalf("old report redirected into replacement: %v", err)
	}
	if len(fixture.activity(t, fixture.input.Creation).Pending.NextStep) != 0 {
		t.Fatal("rejected report reached parent")
	}
	if _, err := messaging.Report(tools.WithAgentCreationID(t.Context(), current.Creation.CreationID), report.Report{
		ChildSessionID: current.Creation.AgentID,
		Output:         "current report",
		Delivery:       report.Quiet,
	}); err != nil {
		t.Fatal(err)
	}
}
