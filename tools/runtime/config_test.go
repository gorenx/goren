package toolruntime

import (
	"testing"

	"github.com/gorenx/goren/llm"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

func testSchema(name string) llm.ToolSchema {
	return llm.ToolSchema{
		Name:        name,
		Description: name,
		Parameters:  []byte(`{"type":"object"}`),
	}
}

func TestMergeInheritsSchemaAndOverridesIndividualPolicyFields(t *testing.T) {
	disabled := false
	visible := true
	input := toolregistry.Configuration{
		Global: toolregistry.Catalog{
			Schemas: []llm.ToolSchema{testSchema("read"), testSchema("write")},
			Policies: map[string]toolregistry.Policy{
				"read": {Visible: &disabled},
			},
		},
		Custom: toolregistry.Catalog{
			Schemas: []llm.ToolSchema{
				{
					Name:        "read",
					Description: "custom read",
					Parameters:  []byte(`{"type":"object"}`),
				},
			},
			Policies: map[string]toolregistry.Policy{
				"read":  {Visible: &visible},
				"write": {Enabled: &disabled},
			},
		},
	}
	merged, err := Merge(input)
	if err != nil || len(merged) != 1 || !merged[0].Visible || merged[0].Schema.Description != "custom read" {
		t.Fatalf("merge=%+v error=%v", merged, err)
	}
	merged[0].Schema.Parameters[0] = '!'
	if input.Custom.Schemas[0].Parameters[0] != '{' {
		t.Fatal("merged schema aliases Registry input")
	}
	if input.Global.Schemas[0].Description != "read" {
		t.Fatal("custom schema mutated global schema")
	}
}

func TestMergeRejectsPolicyWithoutSchema(t *testing.T) {
	disabled := false
	_, err := Merge(toolregistry.Configuration{
		Custom: toolregistry.Catalog{
			Policies: map[string]toolregistry.Policy{
				"missing": {Enabled: &disabled},
			},
		},
	})
	if err == nil {
		t.Fatal("unknown tool policy was accepted")
	}
}
