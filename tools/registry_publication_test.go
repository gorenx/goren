package tools_test

import (
	"slices"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools"
)

func candidate(name string, visible bool) tools.Candidate {
	return tools.Candidate{
		Schema: llm.ToolSchema{
			Name:       name,
			Parameters: []byte(`{"type":"object"}`),
		},
		Visible: visible,
	}
}

func TestRegistryPublishesCompleteCatalogAndRetainsOutcome(t *testing.T) {
	var catalog tools.Registry
	input := tools.Update{
		ID:      "first",
		AgentID: "agent",
		Names:   []string{"visible", "hidden"},
	}
	if err := catalog.Begin(input); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Begin(input); err != nil {
		t.Fatal(err)
	}
	input.Names[0] = "mutated"
	original := candidate("visible", true)
	if err := catalog.Register("first", original); err != nil {
		t.Fatal(err)
	}
	original.Schema.Parameters[0] = '!'
	if _, err := catalog.Publish("first"); err == nil {
		t.Fatal("partial catalog published")
	}
	if _, exists := catalog.Read("agent"); exists {
		t.Fatal("candidate exposed as baseline")
	}
	if outcome, exists := catalog.Lookup("first"); !exists || outcome.Published {
		t.Fatalf("lookup = %#v, %v", outcome, exists)
	}
	if err := catalog.Settle("first"); err == nil {
		t.Fatal("unpublished update settled")
	}
	if err := catalog.Register("first", candidate("hidden", false)); err != nil {
		t.Fatal(err)
	}
	committed, err := catalog.Publish("first")
	if err != nil {
		t.Fatal(err)
	}
	if committed.Version != 1 || committed.AgentID != "agent" ||
		!slices.Equal(committed.Definitions.KnownNames, []string{"visible", "hidden"}) ||
		len(committed.Definitions.Schemas) != 1 || committed.Definitions.Schemas[0].Name != "visible" {
		t.Fatalf("publication = %#v", committed)
	}
	committed.Definitions.Schemas[0].Parameters[0] = '!'
	committed.Definitions.KnownNames[0] = "mutated"
	repeated, err := catalog.Publish("first")
	if err != nil || repeated.Version != 1 || repeated.Definitions.KnownNames[0] != "visible" ||
		string(repeated.Definitions.Schemas[0].Parameters) != `{"type":"object"}` {
		t.Fatalf("retry = %#v, %v", repeated, err)
	}
	if err := catalog.Abort("first"); err == nil {
		t.Fatal("published update aborted")
	}
	if err := catalog.Begin(tools.Update{
		ID:              "blocked",
		AgentID:         "agent",
		ExpectedVersion: 1,
	}); err == nil {
		t.Fatal("unsettled update bypassed")
	}
	if outcome, exists := catalog.Lookup("first"); !exists || !outcome.Published || outcome.Catalog.Version != 1 {
		t.Fatalf("published lookup = %#v, %v", outcome, exists)
	}
	if err := catalog.Settle("first"); err != nil {
		t.Fatal(err)
	}
	if _, exists := catalog.Lookup("first"); exists {
		t.Fatal("settled batch retained")
	}
	if err := catalog.Begin(tools.Update{
		ID:      "stale",
		AgentID: "agent",
	}); err == nil {
		t.Fatal("stale version accepted")
	}
	if err := catalog.Begin(tools.Update{
		ID:              "empty",
		AgentID:         "agent",
		ExpectedVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	empty, err := catalog.Publish("empty")
	if err != nil || empty.Version != 2 || len(empty.Definitions.KnownNames) != 0 || len(empty.Definitions.Schemas) != 0 {
		t.Fatalf("empty publication = %#v, %v", empty, err)
	}
}

func TestRegistryRejectsConflictsWithoutChangingBaseline(t *testing.T) {
	var catalog tools.Registry
	first := tools.Update{
		ID:      "baseline",
		AgentID: "agent",
	}
	if err := catalog.Begin(first); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Publish(first.ID); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Settle(first.ID); err != nil {
		t.Fatal(err)
	}
	next := tools.Update{
		ID:              "next",
		AgentID:         "agent",
		ExpectedVersion: 1,
		Names:           []string{"echo"},
	}
	if err := catalog.Begin(next); err != nil {
		t.Fatal(err)
	}
	conflicting := next
	conflicting.Names = []string{"other"}
	if err := catalog.Begin(conflicting); err == nil {
		t.Fatal("conflicting begin accepted")
	}
	invalid := candidate("echo", true)
	invalid.Schema.Parameters = []byte(`{"type":"string"}`)
	if err := catalog.Register(next.ID, invalid); err == nil {
		t.Fatal("invalid schema accepted")
	}
	if err := catalog.Register(next.ID, candidate("other", true)); err == nil {
		t.Fatal("unexpected tool accepted")
	}
	if err := catalog.Register(next.ID, candidate("echo", true)); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Register(next.ID, candidate("echo", true)); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Register(next.ID, candidate("echo", false)); err == nil {
		t.Fatal("conflicting candidate accepted")
	}
	before, exists := catalog.Read("agent")
	if !exists || before.Version != 1 || len(before.Definitions.Schemas) != 0 {
		t.Fatalf("baseline changed: %#v", before)
	}
	if err := catalog.Begin(tools.Update{
		ID:      "independent",
		AgentID: "second",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Publish("independent"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Abort(next.ID); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Register(next.ID, candidate("echo", true)); err == nil {
		t.Fatal("late registration recreated batch")
	}
	if _, err := catalog.Publish(next.ID); err == nil {
		t.Fatal("aborted batch published")
	}
	after, _ := catalog.Read("agent")
	if after.Version != before.Version {
		t.Fatal("abort changed baseline")
	}
}

func TestAbortedUpdateCannotRegisterIntoItsReplacement(t *testing.T) {
	var catalog tools.Registry
	for _, identifier := range []tools.UpdateID{"aborted", "replacement"} {
		if err := catalog.Begin(tools.Update{
			ID:      identifier,
			AgentID: "agent",
			Names:   []string{"echo"},
		}); err != nil {
			t.Fatal(err)
		}
		if identifier == "aborted" {
			if err := catalog.Abort(identifier); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := catalog.Register("aborted", candidate("echo", true)); err == nil {
		t.Fatal("late registration from aborted update accepted")
	}
	if _, err := catalog.Publish("replacement"); err == nil {
		t.Fatal("old candidate contaminated replacement update")
	}
	if err := catalog.Register("replacement", candidate("echo", true)); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Publish("replacement"); err != nil {
		t.Fatal(err)
	}
}
