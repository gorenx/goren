package systemprompt

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/llm"
)

func TestRuntimeConsumesOnlyItsAgentTools(t *testing.T) {
	owner, err := NewRuntime("agent-a", Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := owner.Assemble(ctx); !errors.Is(err, ErrToolsNotReady) {
		t.Fatalf("uninitialized assembly: %v", err)
	}
	definitions := ToolDefinitions{
		Schemas: []llm.ToolSchema{
			{
				Version:    7,
				Name:       "read",
				Parameters: json.RawMessage(`{"type":"object"}`),
			},
		},
	}
	if err := owner.ApplyTools("agent-b", definitions); !errors.Is(err, ErrAgent) {
		t.Fatalf("cross-Agent update: %v", err)
	}
	if err := owner.ApplyTools("agent-a", definitions); err != nil {
		t.Fatal(err)
	}
	definitions.Schemas[0].Name = "changed"
	definitions.Schemas[0].Parameters[0] = 'x'

	assembled, err := owner.Assemble(ctx)
	if err != nil || len(assembled.Tools) != 1 || assembled.Tools[0].Name != "read" || assembled.Tools[0].Version != 7 || !json.Valid(assembled.Tools[0].Parameters) {
		t.Fatalf("event isolation: %#v, %v", assembled.Tools, err)
	}

	if err := owner.ApplyTools("agent-a", definitions); err == nil {
		t.Fatal("invalid new definitions accepted")
	}
	if _, err := owner.Assemble(ctx); err != nil {
		t.Fatalf("failed update lost valid view: %v", err)
	}
	if err := owner.ApplyTools("agent-a", ToolDefinitions{}); err != nil {
		t.Fatal(err)
	}
	empty, err := owner.Assemble(ctx)
	if err != nil || len(empty.Tools) != 0 {
		t.Fatalf("empty collection: %#v, %v", empty.Tools, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := owner.Assemble(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled assembly: %v", err)
	}
}
