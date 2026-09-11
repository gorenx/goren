package systemprompt

import (
	"context"
	"testing"
)

type observedText struct {
	calls int
}

func (source *observedText) ResolveText(context.Context, AssembleContext) (string, error) {
	source.calls++
	return "runtime context", nil
}

func TestSuppressionSkipsContextProvidersAcrossCapturedLayers(t *testing.T) {
	enabled := false
	settings, err := ValidateConfig(Config{
		IncludeRuntimeContext: &enabled,
		Persona:               "system text",
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := NewLayer(settings)
	if err != nil {
		t.Fatal(err)
	}
	child := DeriveLayer(root.Capture())
	source := &observedText{}
	if _, err := child.AddContext(context.Background(), PromptContext{
		Name: "dynamic-context",
		Text: source,
	}); err != nil {
		t.Fatal(err)
	}
	assembled, err := child.Assemble(context.Background(), AssembleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if source.calls != 0 || assembled.Contexts == nil || len(assembled.Contexts) != 0 {
		t.Fatalf("suppressed context was evaluated: calls=%d contexts=%#v", source.calls, assembled.Contexts)
	}
	if len(assembled.Sections) == 0 {
		t.Fatal("suppression removed system sections")
	}
	child.SetBase(Snapshot{})
	assembled, err = child.Assemble(context.Background(), AssembleContext{})
	if err != nil || source.calls != 1 || len(assembled.Contexts) != 1 {
		t.Fatalf("explicit baseline update did not restore context: calls=%d, %v", source.calls, err)
	}
}
