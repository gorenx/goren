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
