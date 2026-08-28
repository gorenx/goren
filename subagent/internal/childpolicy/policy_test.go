package childpolicy

import (
	"context"
	"reflect"
	"testing"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

func TestPluginsSeedDelegationOnlyForFreshChild(t *testing.T) {
	t.Parallel()
	personaText := "review carefully"
	delegation := &delegationRecord{}
	selected := PolicySet{
		Delegation: delegation,
		Persona:    &personaText,
		ToolRestriction: &tools.ToolRestriction{
			Allow: []string{"read"},
		},
	}
	fresh := Plugins(selected)
	selected.Delegation = nil
	resumed := Plugins(selected)
	if len(fresh) != 3 || len(resumed) != 2 {
		t.Fatalf(
			"child policy Plugins fresh=%d resumed=%d",
			len(fresh),
			len(resumed),
		)
	}
	if _, matches := fresh[0].(*delegationPolicy); !matches {
		t.Fatalf("first fresh Plugin = %T, want delegation policy", fresh[0])
	}
	if _, matches := resumed[0].(*persona); !matches {
		t.Fatalf("first resumed Plugin = %T, want persona", resumed[0])
	}
	if _, matches := resumed[1].(*toolRestriction); !matches {
		t.Fatalf("second resumed Plugin = %T, want Tool restriction", resumed[1])
	}
}

func TestBoundSystemPromptReplacesInheritedIdentity(t *testing.T) {
	t.Parallel()
	validated, err := systemprompt.ValidateConfig(systemprompt.Config{
		Persona: "parent identity",
	})
	if err != nil {
		t.Fatal(err)
	}
	prompts := systemprompt.New(
		validated,
		systemprompt.RegistryOptions{},
	)
	boundPrompt := newBoundSystemPrompt("review parent interactions")
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err = runtimeEngine.Start(
		context.Background(),
		prompts,
		boundPrompt,
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if shutdownErr := runtimeEngine.Shutdown(
			context.Background(),
		); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})

	assembled, err := prompts.Assemble(
		context.Background(),
		systemprompt.AssembleContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantSections := []systemprompt.AssembledSection{
		{
			Name: boundSystemPromptSection,
			Text: "review parent interactions",
		},
	}
	if !reflect.DeepEqual(assembled.Sections, wantSections) {
		t.Fatalf("assembled sections = %#v, want %#v", assembled.Sections, wantSections)
	}
}

type delegationRecord struct {
	plugin.Base
}

func (*delegationRecord) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "test/delegation-policy",
	}
}

func (*delegationRecord) Apply(context.Context) error   { return nil }
func (*delegationRecord) Dispose(context.Context) error { return nil }
func (*delegationRecord) SeedDelegationPolicy(
	context.Context,
	session.Context,
) error {
	return nil
}
