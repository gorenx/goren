package systemprompt

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gorenx/goren/llm"
)

func TestBaselineUpdatePreservesRegistrationsAndCapturedSnapshots(t *testing.T) {
	ctx := context.Background()
	validated, err := ValidateConfig(Config{
		Persona: "old persona",
	})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewLayer(validated)
	if err != nil {
		t.Fatal(err)
	}
	extension, err := owner.AddSection(ctx, PromptSection{
		Name: "extension",
		Text: StaticText("extension instruction"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.AddContext(ctx, PromptContext{
		Name: "environment",
		Text: StaticText("runtime context"),
	}); err != nil {
		t.Fatal(err)
	}
	before := owner.Capture()
	disabled := false
	order := []string{"z", ToolOrderRest}
	if err := owner.ApplyBaseline(ctx, Config{
		Persona:                "new persona",
		IncludeHarnessIdentity: &disabled,
		IncludeRuntimeContext:  &disabled,
		ToolOrder:              order,
	}); err != nil {
		t.Fatal(err)
	}
	order[0] = "caller mutation"
	input := AssembleContext{
		Tools: ToolDefinitions{
			Schemas: []llm.ToolSchema{
				{
					Name:       "a",
					Parameters: []byte(`{"type":"object"}`),
				},
				{
					Name:       "z",
					Parameters: []byte(`{"type":"object"}`),
				},
			},
			KnownNames: []string{"a", "z"},
		},
	}
	previous, err := before.Assemble(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	current, err := owner.Assemble(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	oldText, _ := RenderPrompt(previous)
	newText, _ := RenderPrompt(current)
	if !strings.Contains(oldText, "old persona") ||
		strings.Contains(oldText, "new persona") ||
		len(previous.Contexts) != 1 ||
		previous.Tools[0].Name != "a" {
		t.Fatalf("old snapshot changed: %#v", previous)
	}
	if !strings.Contains(newText, "new persona") ||
		!strings.Contains(newText, "extension instruction") ||
		strings.Contains(newText, harnessIdentityText) ||
		len(current.Contexts) != 0 ||
		current.Tools[0].Name != "z" {
		t.Fatalf("new baseline not applied: %#v", current)
	}
	if removed, err := owner.Remove(ctx, extension); err != nil || !removed {
		t.Fatalf("unrelated registration ID lost: %v %v", removed, err)
	}
	if err := owner.ApplyBaseline(ctx, Config{}); err != nil {
		t.Fatal(err)
	}
	restored, err := owner.Assemble(ctx, input)
	if err != nil || len(restored.Contexts) != 1 {
		t.Fatalf("context registration was deleted instead of suppressed: %#v %v", restored, err)
	}
}

func TestBaselineUpdateRejectsInvalidAndConflictingCandidatesAtomically(t *testing.T) {
	ctx := context.Background()
	disabled := false
	validated, err := ValidateConfig(Config{
		IncludeHarnessIdentity: &disabled,
		Persona:                "retained",
	})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewLayer(validated)
	if err != nil {
		t.Fatal(err)
	}
	// An extension owns this name while the built-in identity is disabled.
	identifier, err := owner.AddSection(ctx, PromptSection{
		Name: harnessIdentityName,
		Text: StaticText("extension-owned identity"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.ApplyBaseline(ctx, Config{
		Persona: "must not publish",
	}); err == nil {
		t.Fatal("conflicting built-in replaced extension registration")
	}
	if err := owner.ApplyBaseline(ctx, Config{
		ToolOrder: []string{},
	}); err == nil {
		t.Fatal("invalid config accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := owner.ApplyBaseline(canceled, Config{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	current, err := owner.Assemble(ctx, AssembleContext{})
	if err != nil {
		t.Fatal(err)
	}
	textValue, _ := RenderPrompt(current)
	if !strings.Contains(textValue, "retained") || strings.Contains(textValue, "must not publish") {
		t.Fatalf("failed candidate leaked: %s", textValue)
	}
	if removed, err := owner.Remove(ctx, identifier); !removed || err != nil {
		t.Fatalf("failed update changed removal identity: %v %v", removed, err)
	}
	child := DeriveLayer(owner.Capture())
	if err := child.ApplyBaseline(ctx, Config{}); err == nil {
		t.Fatal("child accepted root configuration replacement")
	}
}
