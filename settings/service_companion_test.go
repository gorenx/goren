package settings

import (
	"errors"
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/tools"
	"reflect"
	"testing"

	"github.com/gorenx/goren/subagent/companion"
)

func TestCompanionDirectoryReplacementResetAndIsolation(t *testing.T) {
	baseline := exampleBaseline()
	baseline.Agents = map[string]Values{"local": {}}
	baseline.Companions = map[string]companion.Config{
		"reviewer": {
			Enabled:       true,
			Configuration: "local",
		},
	}
	storage := &memoryStore{}
	prompts := &promptRecorder{}
	connections := &connectionRecorder{}
	toolsets := &toolRecorder{}
	owner, err := NewService(baseline, State{}, storage, prompts, connections, toolsets)
	if err != nil {
		t.Fatal(err)
	}
	identifier := Group{
		Section: CompanionSection,
		Name:    "reviewer",
	}
	view, err := owner.UpdateCompanion(t.Context(), "reviewer", 0, companion.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if view.Target.Companions["reviewer"].Enabled || view.Target.Companions["reviewer"].Configuration != "" {
		t.Fatal("replacement inherited omitted local values")
	}
	if view.Applications[identifier].AppliedRevision != 1 {
		t.Fatal("directory revision not adopted")
	}
	view, err = owner.ResetCompanion(t.Context(), "reviewer", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Target.Companions["reviewer"].Enabled || view.State.Version(identifier) != 2 || view.State.User.Has(identifier) {
		t.Fatal("reset lost baseline or tombstone")
	}
	if _, err := owner.UpdateCompanion(t.Context(), "reviewer", 1, companion.Config{}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale edit accepted after reset")
	}
	if _, err := owner.UpdateCompanion(t.Context(), "new", 0, companion.Config{
		Inherit: companion.Inheritance{
			Tools: "some",
		},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatal("partial tool inheritance accepted")
	}
	if _, err := owner.RetryApplication(t.Context(), identifier, 2); err != nil {
		t.Fatal(err)
	}
	if prompts.calls != 0 || connections.calls != 0 || toolsets.calls != 0 {
		t.Fatal("directory mutation applied configuration to running owners")
	}
	if storage.stored.Revision != 2 {
		t.Fatal("rejected edit or retry wrote a revision")
	}
}

func TestNamedAgentValuesAreIndependentAndUseExistingSettingsTransaction(t *testing.T) {
	baseline := exampleBaseline()
	storage := &memoryStore{}
	prompts := &promptRecorder{}
	connections := &connectionRecorder{}
	toolsets := &toolRecorder{}
	owner, err := NewService(baseline, State{}, storage, prompts, connections, toolsets)
	if err != nil {
		t.Fatal(err)
	}
	input := Values{
		Model: agent.Options{
			MaxTokens: new(400),
		},
		Prompts: map[string]PromptConfig{"default": {
			Persona:   new("private"),
			ToolOrder: new([]string{"<unlisted-tools>"}),
		}},
		Tools: map[string]tools.Config{"read": {
			Visible: new(false),
		}},
		MCP: map[string]MCPConfig{"local": {
			Enabled: true,
			Connection: mcpclient.Config{
				ServerName: "local",
				Transport:  "stdio",
				Command:    "fixture",
				Args:       []string{"original"},
			},
		}},
	}
	first, err := owner.UpdateAgent(t.Context(), "review-config", 0, input)
	if err != nil {
		t.Fatal(err)
	}
	*input.Model.MaxTokens = 800
	*input.Prompts["default"].Persona = "changed"
	(*input.Prompts["default"].ToolOrder)[0] = "changed"
	*input.Tools["read"].Visible = true
	input.MCP["local"].Connection.Args[0] = "changed"
	saved := first.Target.Agents["review-config"]
	if *saved.Model.MaxTokens != 400 || *saved.Prompts["default"].Persona != "private" || (*saved.Prompts["default"].ToolOrder)[0] != "<unlisted-tools>" || *saved.Tools["read"].Visible || saved.MCP["local"].Connection.Args[0] != "original" {
		t.Fatal("saved capability values alias caller input")
	}
	*saved.Prompts["default"].Persona = "edited view"
	if *owner.Snapshot().Target.Agents["review-config"].Prompts["default"].Persona != "private" {
		t.Fatal("query exposes mutable owner state")
	}
	if _, err := owner.UpdateCompanion(t.Context(), "reviewer", 0, companion.Config{
		Enabled:       true,
		Configuration: "missing",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatal("dangling configuration reference accepted")
	}
	if _, err := owner.UpdateCompanion(t.Context(), "reviewer", 0, companion.Config{
		Enabled:       true,
		Configuration: "review-config",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.UpdateAgent(t.Context(), "review-config", 0, Values{}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale replacement accepted")
	}
	replaced, err := owner.UpdateAgent(t.Context(), "review-config", 1, Values{})
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced.Target.Agents["review-config"].Prompts) != 0 || len(replaced.Target.Agents["review-config"].MCP) != 0 {
		t.Fatal("complete replacement merged previous values")
	}
	if !reflect.DeepEqual(replaced.Target.Values, baseline.Values.Clone()) || prompts.calls != 0 || connections.calls != 0 || toolsets.calls != 0 {
		t.Fatal("named configuration changed process defaults or running resources")
	}
	if storage.stored.Revision != 3 {
		t.Fatal("rejected writes changed transaction version")
	}
}
