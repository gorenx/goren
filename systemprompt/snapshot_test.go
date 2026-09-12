package systemprompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gorenx/goren/systemprompt"
)

func renderSnapshot(t *testing.T, prepared systemprompt.Snapshot) string {
	t.Helper()
	assembled, err := prepared.Assemble(context.Background(), systemprompt.AssembleContext{})
	if err != nil {
		t.Fatal(err)
	}
	textValue, err := systemprompt.RenderPrompt(assembled)
	if err != nil {
		t.Fatal(err)
	}
	return textValue
}

func TestCapturedSnapshotSurvivesRemovalAndOwnerClose(t *testing.T) {
	prompts := newPrompts(t, systemprompt.Config{})
	registration, err := prompts.AddSection(context.Background(), systemprompt.PromptSection{
		Name: "captured",
		Text: systemprompt.StaticText("captured-text"),
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared := prompts.Capture()
	if _, err := prompts.Remove(context.Background(), registration); err != nil {
		t.Fatal(err)
	}
	if err := prompts.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	original := renderSnapshot(t, prepared)
	if !strings.Contains(original, "captured-text") {
		t.Fatal("captured request changed after removal")
	}
	next := renderSnapshot(t, prompts.Capture())
	if strings.Contains(next, "captured-text") {
		t.Fatal("next request retained removed registrations")
	}
}

func TestChildUsesSnapshotUntilExplicitBaseUpdate(t *testing.T) {
	prompts := newPrompts(t, systemprompt.Config{})
	child := systemprompt.DeriveLayer(prompts.Capture())
	inherited := child.Capture()
	if _, err := prompts.AddSection(context.Background(), systemprompt.PromptSection{
		Name: "later",
		Text: systemprompt.StaticText("later-definition"),
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(renderSnapshot(t, child.Capture()), "later-definition") {
		t.Fatal("child traversed a live parent")
	}
	child.SetBase(prompts.Capture())
	if !strings.Contains(renderSnapshot(t, child.Capture()), "later-definition") {
		t.Fatal("explicit base update was not applied")
	}
	if strings.Contains(renderSnapshot(t, inherited), "later-definition") {
		t.Fatal("base update rewrote an existing request")
	}
}

func TestSnapshotConfigurationRetainsRegisteredDefinitions(t *testing.T) {
	root := newPrompts(t, systemprompt.Config{
		Persona: "original persona",
	})
	registration, err := root.AddSection(t.Context(), systemprompt.PromptSection{
		Name: "extension",
		Text: systemprompt.StaticText("extension text"),
	})
	if err != nil {
		t.Fatal(err)
	}
	child := systemprompt.DeriveLayer(root.Capture())
	if _, err := child.AddSection(t.Context(), systemprompt.PromptSection{
		Name: "report",
		Text: systemprompt.StaticText("report to parent"),
	}); err != nil {
		t.Fatal(err)
	}
	before := child.Capture()
	selected, err := before.WithConfiguration(systemprompt.Config{
		Persona: "selected persona",
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered := renderSnapshot(t, selected)
	for _, textValue := range []string{"selected persona", "extension text", "report to parent"} {
		if !strings.Contains(rendered, textValue) {
			t.Fatalf("missing %q in %s", textValue, rendered)
		}
	}
	if strings.Contains(rendered, "original persona") || !strings.Contains(renderSnapshot(t, before), "original persona") {
		t.Fatal("configuration selection did not isolate snapshots")
	}
	if removed, err := root.Remove(t.Context(), registration); err != nil || !removed {
		t.Fatalf("extension ownership changed: %v, %v", removed, err)
	}
	if !strings.Contains(renderSnapshot(t, selected), "extension text") {
		t.Fatal("live removal changed the selected snapshot")
	}
}
