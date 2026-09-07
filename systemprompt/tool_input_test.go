package systemprompt

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/llm"
)

func TestAssemblyUsesDetachedToolDefinitions(t *testing.T) {
	settings, err := ValidateConfig(Config{
		ToolOrder: []string{"hidden", "write", ToolOrderRest},
	})
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := NewLayer(settings)
	if err != nil {
		t.Fatal(err)
	}
	definitions := ToolDefinitions{
		Schemas: []llm.ToolSchema{
			{
				Name:       "read",
				Parameters: json.RawMessage(`{"type":"object"}`),
			},
			{
				Name:       "write",
				Parameters: json.RawMessage(`{"type":"object"}`),
			},
		},
		KnownNames: []string{"read", "write", "hidden"},
	}
	assembled, err := prompts.Capture().Assemble(context.Background(), AssembleContext{
		Tools: definitions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(assembled.Tools) != 2 || assembled.Tools[0].Name != "write" || assembled.Tools[1].Name != "read" {
		t.Fatalf("ordered tools = %#v", assembled.Tools)
	}
	definitions.Schemas[1].Name = "changed"
	definitions.Schemas[1].Parameters[0] = 'x'
	if assembled.Tools[0].Name != "write" || !json.Valid(assembled.Tools[0].Parameters) {
		t.Fatal("assembly retained mutable tool definitions")
	}
	if _, err := prompts.Capture().Assemble(context.Background(), AssembleContext{}); err == nil {
		t.Fatal("missing required tool names reused earlier definitions")
	}
}
