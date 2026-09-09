package toolregistry_test

import (
	"encoding/json"
	"github.com/gorenx/goren/tools"
	"slices"
	"strings"
	"testing"

	"github.com/gorenx/goren/llm"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

func candidate(name string, visible bool) toolregistry.Candidate {
	return toolregistry.Candidate{
		Schema: llm.ToolSchema{
			Name:       name,
			Parameters: []byte(`{"type":"object"}`),
		},
		Visible: visible,
	}
}

func TestRegistryPublishesCompleteCatalogAndRetainsOutcome(t *testing.T) {
	var catalog toolregistry.Registry
	input := toolregistry.Update{
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
	if committed.AgentID != "agent" ||
		!slices.Equal(committed.KnownNames, []string{"visible", "hidden"}) ||
		len(committed.Schemas) != 1 || committed.Schemas[0].Name != "visible" {
		t.Fatalf("publication = %#v", committed)
	}
	committed.Schemas[0].Parameters[0] = '!'
	committed.KnownNames[0] = "mutated"
	repeated, err := catalog.Publish("first")
	if err != nil || repeated.KnownNames[0] != "visible" ||
		string(repeated.Schemas[0].Parameters) != `{"type":"object"}` {
		t.Fatalf("retry = %#v, %v", repeated, err)
	}
	if err := catalog.Abort("first"); err == nil {
		t.Fatal("published update aborted")
	}
	if err := catalog.Begin(toolregistry.Update{
		ID:      "blocked",
		AgentID: "agent",
	}); err == nil {
		t.Fatal("unsettled update bypassed")
	}
	if outcome, exists := catalog.Lookup("first"); !exists || !outcome.Published || outcome.Catalog.Schemas[0].Version != 1 {
		t.Fatalf("published lookup = %#v, %v", outcome, exists)
	}
	if err := catalog.Settle("first"); err != nil {
		t.Fatal(err)
	}
	if _, exists := catalog.Lookup("first"); exists {
		t.Fatal("settled batch retained")
	}

	if err := catalog.Begin(toolregistry.Update{
		ID:      "empty",
		AgentID: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	empty, err := catalog.Publish("empty")
	if err != nil || len(empty.KnownNames) != 0 || len(empty.Schemas) != 0 {
		t.Fatalf("empty publication = %#v, %v", empty, err)
	}
}

func TestRegistryRejectsConflictsWithoutChangingBaseline(t *testing.T) {
	var catalog toolregistry.Registry
	first := toolregistry.Update{
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
	next := toolregistry.Update{
		ID:      "next",
		AgentID: "agent",

		Names: []string{"echo"},
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
	if !exists || len(before.Schemas) != 0 {
		t.Fatalf("baseline changed: %#v", before)
	}
	if err := catalog.Begin(toolregistry.Update{
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
	if !slices.Equal(after.KnownNames, before.KnownNames) {
		t.Fatal("abort changed baseline")
	}
}

func TestAbortedUpdateCannotRegisterIntoItsReplacement(t *testing.T) {
	var catalog toolregistry.Registry
	for _, identifier := range []toolregistry.UpdateID{"aborted", "replacement"} {
		if err := catalog.Begin(toolregistry.Update{
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

func TestSchemaVersionsAreIndependentAndSurviveRemoval(t *testing.T) {
	var catalog toolregistry.Registry
	publish := func(identifier toolregistry.UpdateID, entries ...toolregistry.Candidate) tools.Definitions {
		t.Helper()
		names := make([]string, len(entries))
		for index, entry := range entries {
			names[index] = entry.Schema.Name
		}
		err := catalog.Begin(toolregistry.Update{
			ID:      identifier,
			AgentID: "agent",
			Names:   names,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			err = catalog.Register(identifier, entry)
			if err != nil {
				t.Fatal(err)
			}
		}
		result, err := catalog.Publish(identifier)
		if err != nil {
			t.Fatal(err)
		}
		err = catalog.Settle(identifier)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := publish("first", candidate("a", true), candidate("b", true))
	if first.Schemas[0].Version != 1 || first.Schemas[1].Version != 1 {
		t.Fatalf("initial versions: %#v", first)
	}
	changed := candidate("a", true)
	changed.Schema.Description = "changed"
	second := publish("second", changed, candidate("b", true))
	if second.Schemas[0].Version != 2 || second.Schemas[1].Version != 1 {
		t.Fatalf("independent versions: %#v", second)
	}
	repeated := publish("repeated", changed, candidate("b", true))
	if repeated.Schemas[0].Version != 2 || repeated.Schemas[1].Version != 1 {
		t.Fatalf("unchanged definitions: %#v", repeated)
	}
	changed.Visible = false
	hidden := publish("hidden", changed, candidate("b", true))
	if len(hidden.Schemas) != 1 || hidden.Schemas[0].Version != 1 || len(hidden.KnownNames) != 2 {
		t.Fatalf("hidden catalog: %#v", hidden)
	}
	changed.Visible = true
	visible := publish("visible", changed, candidate("b", true))
	if visible.Schemas[0].Version != 2 {
		t.Fatalf("visibility changed schema version: %#v", visible)
	}
	empty := publish("empty")
	baseline, found := catalog.Read("agent")
	if !found || len(empty.Schemas) != 0 || len(baseline.KnownNames) != 0 {
		t.Fatalf("empty publication: %#v, %v", baseline, found)
	}
	restored := publish("restored", changed, candidate("b", true))
	if restored.Schemas[0].Version != 3 || restored.Schemas[1].Version != 2 {
		t.Fatalf("removed versions reused: %#v", restored)
	}
	encoded, err := json.Marshal(restored.Schemas)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "version") || strings.Contains(string(encoded), "Version") {
		t.Fatalf("internal version leaked to model schema: %s", encoded)
	}
	// A returned schema must not alias the Registry's retained history.
	restored.Schemas[0].Parameters[0] = '!'
	intact := publish("intact", changed, candidate("b", true))
	if intact.Schemas[0].Version != 3 {
		t.Fatalf("read result mutated history: %#v", intact)
	}
}

func TestRegistryRejectsProviderAssignedSchemaVersion(t *testing.T) {
	var catalog toolregistry.Registry
	err := catalog.Begin(toolregistry.Update{
		ID:      "update",
		AgentID: "agent",
		Names:   []string{"echo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := candidate("echo", true)
	entry.Schema.Version = 100
	err = catalog.Register("update", entry)
	if err == nil {
		t.Fatal("provider assigned a published version")
	}
	_, err = catalog.Publish("update")
	if err == nil {
		t.Fatal("invalid candidate entered the batch")
	}
	entry.Schema.Version = 0
	err = catalog.Register("update", entry)
	if err != nil {
		t.Fatal(err)
	}
	result, err := catalog.Publish("update")
	if err != nil || result.Schemas[0].Version != 1 {
		t.Fatalf("first version was consumed by failed input: %#v, %v", result, err)
	}
}
