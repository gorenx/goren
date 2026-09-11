package toolregistry_test

import (
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

type configGuard struct{}

func (*configGuard) DenyReason(tools.ToolExecution) (string, bool) { return "retained guard", true }

func TestRegistryToolConfigurationPreservesSourceCapabilities(t *testing.T) {
	var directory toolregistry.Registry
	hidden := false
	guard := &configGuard{}
	source := toolregistry.Catalog{
		Schemas: []llm.ToolSchema{schema("read", "read"), schema("other", "other")},
		ProviderIDs: map[string]tools.ProviderID{
			"read": "first",
		},
		Policies: map[string]toolregistry.Policy{
			"read": {
				Visible: &hidden,
				Guards:  []tools.ToolGuard{guard},
			},
		},
	}
	if err := directory.RegisterSource("source", source); err != nil {
		t.Fatal(err)
	}
	disabled := false
	if err := directory.ConfigureTool("read", tools.Config{
		Enabled: &disabled,
	}); err != nil {
		t.Fatal(err)
	}
	disabled = true
	current := directory.Read("agent").Global
	policy := current.Policies["read"]
	if *policy.Enabled ||
		!*policy.Visible ||
		policy.Guards[0] != guard {
		t.Fatal("configuration inherited a source flag, aliased input, or replaced a guard")
	}
	if current.ProviderIDs["read"] != "first" || current.Schemas[0].Version != 1 {
		t.Fatal("configuration changed schema or binding")
	}
	if current.Policies["other"].Enabled != nil {
		t.Fatal("configuration changed another tool")
	}
	source.ProviderIDs["read"] = "second"
	if err := directory.RegisterSource("source", source); err != nil {
		t.Fatal(err)
	}
	current = directory.Read("agent").Global
	if *current.Policies["read"].Enabled || current.ProviderIDs["read"] != "second" {
		t.Fatal("source republication erased configuration or lost binding")
	}
	if err := directory.ConfigureTool("read", tools.Config{}); err != nil {
		t.Fatal(err)
	}
	current = directory.Read("agent").Global
	if !*current.Policies["read"].Enabled || !*current.Policies["read"].Visible {
		t.Fatal("empty complete configuration inherited earlier flags")
	}
	directory.RemoveSource("source")
	if len(directory.Read("agent").Global.Schemas) != 0 || len(directory.Read("agent").Global.Policies) != 0 {
		t.Fatal("withdrawal left a configured ghost tool")
	}
	if err := directory.ConfigureTool("read", tools.Config{}); err == nil {
		t.Fatal("unknown tool accepted")
	}
}
