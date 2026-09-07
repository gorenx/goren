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
	if _, err := owner.Assemble(ctx, 1); !errors.Is(err, ErrToolsNotReady) {
		t.Fatalf("uninitialized assembly: %v", err)
	}
	definitions := ToolDefinitions{
		Schemas: []llm.ToolSchema{
			{
				Name:       "read",
				Parameters: json.RawMessage(`{"type":"object"}`),
			},
		},
	}
	if err := owner.ApplyTools("agent-b", 1, definitions); !errors.Is(err, ErrAgent) {
		t.Fatalf("cross-Agent update: %v", err)
	}
	if err := owner.ApplyTools("agent-a", 2, definitions); err != nil {
		t.Fatal(err)
	}
	definitions.Schemas[0].Name = "changed"
	definitions.Schemas[0].Parameters[0] = 'x'
	for _, version := range []uint64{1, 2} {
		if err := owner.ApplyTools("agent-a", version, ToolDefinitions{}); err != nil {
			t.Fatal(err)
		}
	}
	assembled, err := owner.Assemble(ctx, 2)
	if err != nil || len(assembled.Tools) != 1 || assembled.Tools[0].Name != "read" || !json.Valid(assembled.Tools[0].Parameters) {
		t.Fatalf("event isolation: %#v, %v", assembled.Tools, err)
	}
	if _, err := owner.Assemble(ctx, 1); !errors.Is(err, ErrToolsVersion) {
		t.Fatalf("stale request: %v", err)
	}
	if err := owner.ApplyTools("agent-a", 3, definitions); err == nil {
		t.Fatal("invalid new definitions accepted")
	}
	if _, err := owner.Assemble(ctx, 2); err != nil {
		t.Fatalf("failed update lost valid view: %v", err)
	}
	if err := owner.ApplyTools("agent-a", 3, ToolDefinitions{}); err != nil {
		t.Fatal(err)
	}
	empty, err := owner.Assemble(ctx, 3)
	if err != nil || len(empty.Tools) != 0 {
		t.Fatalf("empty collection: %#v, %v", empty.Tools, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := owner.Assemble(canceled, 3); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled assembly: %v", err)
	}
}
