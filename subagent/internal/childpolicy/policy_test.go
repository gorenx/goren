package childpolicy

import (
	"testing"

	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/systemprompt"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

func TestApplyAddsDetachedChildPolicy(t *testing.T) {
	persona := "review carefully"
	restriction := &toolregistry.ToolRestriction{
		Allow: []string{"read"},
		Deny:  []string{"write"},
	}
	configuration := subagent.ChildConfiguration{}
	Apply(
		PolicySet{
			Persona:         &persona,
			ToolRestriction: restriction,
		},
		&configuration,
	)
	if len(configuration.PromptSections) != 1 ||
		configuration.PromptSections[0].Name != systemprompt.PersonaSection {
		t.Fatalf("prompt sections = %#v", configuration.PromptSections)
	}
	if configuration.Tools.Restriction == nil ||
		len(configuration.Tools.Restriction.Allow) != 1 ||
		configuration.Tools.Restriction.Allow[0] != "read" ||
		len(configuration.Tools.Restriction.Deny) != 1 ||
		configuration.Tools.Restriction.Deny[0] != "write" {
		t.Fatalf("tool restriction = %#v", configuration.Tools.Restriction)
	}
	restriction.Allow[0] = "changed"
	if configuration.Tools.Restriction.Allow[0] != "read" {
		t.Fatal("child policy retained caller-owned restriction")
	}
}

func TestApplyEmptyPolicyLeavesConfigurationUntouched(t *testing.T) {
	configuration := subagent.ChildConfiguration{}
	Apply(PolicySet{}, &configuration)
	if len(configuration.PromptSections) != 0 || configuration.Tools.Restriction != nil {
		t.Fatalf("configuration = %#v", configuration)
	}
}
