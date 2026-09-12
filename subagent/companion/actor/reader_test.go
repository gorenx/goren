package companionactor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/subagent/tools/control"
	"github.com/gorenx/goren/tools"
)

func listedCompanions(t *testing.T, fixture *stewardship) []control.Entry {
	t.Helper()
	// ToolActor must replace forged upstream credentials with its admitted parent.
	outcome := fixture.invoke(t, tools.WithAgentCreationID(t.Context(), "forged"), fixture.input.Creation, "list_agents", json.RawMessage(`{}`))
	if outcome.Err != nil {
		t.Fatal(outcome.Err)
	}
	encoded, ok := outcome.Result.SuccessValue()
	if !ok {
		detail, _ := outcome.Result.FailureDetail()
		t.Fatalf("listing failed: %+v", detail)
	}
	var rows []control.Entry
	if err := json.Unmarshal(encoded, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestCompanionListingReportsLifecycleThroughToolRuntime(t *testing.T) {
	fixture, _, child := messagingFixture(t)
	if _, err := fixture.client.Create(t.Context(), Create{
		Name: "disabled",
	}); err != nil {
		t.Fatal(err)
	}
	fixture.creator.mu.Lock()
	fixture.creator.failure = errors.New("private startup detail")
	fixture.creator.mu.Unlock()
	if _, err := fixture.client.Create(t.Context(), Create{
		Name:    "failed",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, fixture.client, "failed", StartFailed)
	rows := listedCompanions(t, fixture)
	if len(rows) != 3 {
		t.Fatalf("unexpected rows: %+v", rows)
	}
	expected := map[string]string{
		"reviewer": "idle",
		"disabled": "stopped",
		"failed":   "start_failed",
	}
	for _, row := range rows {
		if row.Kind != "child" || row.Status != expected[row.Label] || row.ID == "" || row.Reason != "" || row.Parent != nil || row.Depth != nil {
			t.Fatalf("incorrect direct Companion view: %+v", row)
		}
	}
	// A child released through ordinary Registry must never remain listed as ready.
	closed := request[agentactor.Closed](t, fixture.system, fixture.authority, agentregistry.CloseAgent{
		Creation: child.Creation,
	})
	if closed.Err != nil {
		t.Fatal(closed.Err)
	}
	rows = listedCompanions(t, fixture)
	found := false
	for _, row := range rows {
		if row.ID == string(child.Creation.AgentID) {
			found = true
			if row.Kind != "diagnostic" || row.Reason != "unavailable" || row.Status != "" {
				t.Fatalf("missing child reported available: %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("unavailable child silently disappeared")
	}
	if _, err := fixture.client.Remove(t.Context(), Remove{
		Name:             child.Companion.Name,
		ExpectedRevision: child.Companion.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, fixture.client, child.Companion.Name, Stopped)
	for _, row := range listedCompanions(t, fixture) {
		if row.ID == string(child.Creation.AgentID) {
			t.Fatal("removed Companion remains a selection target")
		}
	}
}

func TestCompanionListingRejectsModelRoutingAndStaleCredentials(t *testing.T) {
	fixture, _, _ := messagingFixture(t)
	for _, arguments := range []string{`{"parent":"another"}`, `{"scope":"children"}`, `{"scope":"descendants"}`} {
		outcome := fixture.invoke(t, t.Context(), fixture.input.Creation, "list_agents", json.RawMessage(arguments))
		if outcome.Err == nil {
			if _, success := outcome.Result.SuccessValue(); success {
				t.Fatalf("unsupported model routing accepted: %s", arguments)
			}
		}
	}
	observer, err := NewReader(fixture.system, fixture.authority)
	if err != nil {
		t.Fatal(err)
	}
	input := control.Listing{
		ParentSessionID: fixture.input.Creation.AgentID,
		Scope:           "children",
	}
	if _, err := observer.List(t.Context(), input); !errors.Is(err, agentregistry.ErrCreation) {
		t.Fatalf("missing credential accepted: %v", err)
	}
	if _, err := observer.List(tools.WithAgentCreationID(t.Context(), "stale"), input); !errors.Is(err, agentregistry.ErrCreation) {
		t.Fatalf("stale credential accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(tools.WithAgentCreationID(t.Context(), fixture.input.Creation.CreationID))
	cancel()
	if _, err := observer.List(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled listing: %v", err)
	}
	closed := request[agentactor.Closed](t, fixture.system, fixture.authority, agentregistry.CloseAgent{
		Creation: fixture.input.Creation,
	})
	if closed.Err != nil {
		t.Fatal(closed.Err)
	}
	if _, err := observer.List(tools.WithAgentCreationID(t.Context(), fixture.input.Creation.CreationID), input); err == nil {
		t.Fatal("closed parent retained listing access")
	}
}
