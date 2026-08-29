package childpolicy

import (
	"context"
	"reflect"
	"testing"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

func TestPluginsContainOnlyResidentPolicies(t *testing.T) {
	t.Parallel()
	personaText := "review carefully"
	selected := PolicySet{
		Persona: &personaText,
		ToolRestriction: &tools.ToolRestriction{
			Allow: []string{"read"},
		},
	}
	instances := Plugins(selected)
	if len(instances) != 2 {
		t.Fatalf("child policy Plugins = %d, want 2", len(instances))
	}
	if _, matches := instances[0].(*persona); !matches {
		t.Fatalf("first Plugin = %T, want persona", instances[0])
	}
	if _, matches := instances[1].(*toolRestriction); !matches {
		t.Fatalf("second Plugin = %T, want Tool restriction", instances[1])
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
