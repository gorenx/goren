package toolregistry_test

import (
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

type configGuard struct{}

func (*configGuard) DenyReason(tools.ToolExecution) (string, bool) { return "retained guard", true }

func TestRegistryCatalogPreservesSourceCapabilities(t *testing.T) {
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
	registered := directory.Catalog()
	defaults := registered.Policies["read"]
	if defaults.Enabled != nil || defaults.Visible == nil || *defaults.Visible || defaults.Guards[0] != guard {
		t.Fatal("registered definitions included user flags or lost source policy")
	}
	*defaults.Visible = true
	registered.ProviderIDs["read"] = "caller mutation"
	isolated := directory.Catalog()
	if *isolated.Policies["read"].Visible || isolated.ProviderIDs["read"] != "first" {
		t.Fatal("catalog read exposed mutable source values")
	}
	source.ProviderIDs["read"] = "second"
	if err := directory.RegisterSource("source", source); err != nil {
		t.Fatal(err)
	}
	current := directory.Read("agent").Global
	if current.Policies["read"].Enabled != nil || current.ProviderIDs["read"] != "second" {
		t.Fatal("source replacement lost its policy or Provider binding")
	}
	directory.RemoveSource("source")
	if len(directory.Read("agent").Global.Schemas) != 0 || len(directory.Read("agent").Global.Policies) != 0 {
		t.Fatal("withdrawal left a configured ghost tool")
	}
}
