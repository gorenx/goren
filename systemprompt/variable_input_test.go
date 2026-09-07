package systemprompt

import (
	"context"
	"testing"
)

func TestVariableSnapshotsSurviveReplacement(t *testing.T) {
	ctx := context.Background()
	owner := DeriveLayer(Snapshot{})
	if _, err := owner.AddSection(ctx, PromptSection{
		Name: "description",
		Text: StaticText("{{role}}/{{mode}}"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.AddVariable(ctx, "role", VariableValue{
		Value:   "role-a",
		Defined: true,
	}); err != nil {
		t.Fatal(err)
	}
	original, err := owner.AddVariable(ctx, "mode", VariableValue{
		Value:   "mode-a",
		Defined: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	captured := owner.Capture()
	if _, err := owner.Remove(ctx, original); err != nil {
		t.Fatal(err)
	}
	missing := owner.Capture()
	if _, err := owner.AddVariable(ctx, "mode", VariableValue{
		Value:   "mode-b",
		Defined: true,
	}); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name     string
		snapshot Snapshot
		want     string
	}{
		{
			name:     "captured",
			snapshot: captured,
			want:     "role-a/mode-a",
		},
		{
			name:     "updated",
			snapshot: owner.Capture(),
			want:     "role-a/mode-b",
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			assembled, err := scenario.snapshot.Assemble(ctx, AssembleContext{})
			if err != nil {
				t.Fatal(err)
			}
			rendered, err := RenderPrompt(assembled)
			if err != nil || rendered != scenario.want {
				t.Fatalf("rendered = %q, %v", rendered, err)
			}
		})
	}
	assembled, err := missing.Assemble(ctx, AssembleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RenderPrompt(assembled); err == nil {
		t.Fatal("missing variable reused removed value")
	}
}
