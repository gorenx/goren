package toolruntime

import (
	"context"
	"slices"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

type callPolicy struct{}

func (*callPolicy) EvaluateCall(context.Context, tools.PreExecuteRequest) (tools.PreExecuteOutcome, error) {
	return tools.PreExecuteOutcome{}, nil
}

func TestToolsetChangesOnlyAffectedResources(t *testing.T) {
	hidden := false
	disabled := false
	policy := &callPolicy{}
	for _, scenario := range []struct {
		name   string
		input  toolregistry.Catalog
		events []string
	}{
		{
			name: "visibility",
			input: toolregistry.Catalog{
				Schemas: []llm.ToolSchema{testSchema("read"), testSchema("removed")},
				Policies: map[string]toolregistry.Policy{
					"read": {
						Visible: &hidden,
					},
				},
			},
		},
		{
			name: "policy",
			input: toolregistry.Catalog{
				Schemas: []llm.ToolSchema{testSchema("read"), testSchema("removed")},
				Policies: map[string]toolregistry.Policy{
					"read": {
						Before: policy,
					},
				},
			},
		},
		{
			name: "addition",
			input: toolregistry.Catalog{
				Schemas: []llm.ToolSchema{testSchema("added"), testSchema("read"), testSchema("removed")},
			},
			events: []string{"prepare:added", "commit:added", "retire:added"},
		},
		{
			name: "replacement",
			input: toolregistry.Catalog{
				Schemas: []llm.ToolSchema{changedReadSchema(), testSchema("removed")},
			},
			events: []string{"prepare:read", "commit:read", "retire:read"},
		},
		{
			name: "removal",
			input: toolregistry.Catalog{
				Schemas: []llm.ToolSchema{testSchema("read")},
			},
			events: []string{"remove:removed"},
		},
		{
			name: "disable",
			input: toolregistry.Catalog{
				Schemas: []llm.ToolSchema{testSchema("read"), testSchema("removed")},
				Policies: map[string]toolregistry.Policy{
					"removed": {
						Enabled: &disabled,
					},
				},
			},
			events: []string{"remove:removed"},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			owner, source, endpoint := initializedRuntime(t)
			if err := source.registry.RegisterSource("local", scenario.input); err != nil {
				t.Fatal(err)
			}
			complete, err := owner.Apply(t.Context(), source.registry.Read("agent"), endpoint)
			if !complete || err != nil {
				t.Fatalf("apply: %v %v", complete, err)
			}
			if !slices.Equal(endpoint.events, scenario.events) {
				t.Fatalf("resource operations: %v", endpoint.events)
			}
			captured, err := owner.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if scenario.name == "visibility" && (len(captured.Schemas) != 1 || len(captured.Names) != 2) {
				t.Fatal("visibility not applied")
			}
			if scenario.name == "policy" && captured.Policies["read"].Before != policy {
				t.Fatal("policy not applied")
			}
			endpoint.events = nil
			if complete, err := owner.Apply(t.Context(), source.registry.Read("agent"), endpoint); !complete || err != nil || len(endpoint.events) != 0 {
				t.Fatalf("unchanged target repeated resource work: %v, %v", endpoint.events, err)
			}
		})
	}
}
