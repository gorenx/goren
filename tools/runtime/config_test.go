package toolruntime

import (
	"slices"
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

func TestMergeAppliesSessionToolRestriction(t *testing.T) {
	testCases := []struct {
		name        string
		restriction *toolregistry.ToolRestriction
		want        []string
	}{
		{
			name: "nil allow inherits all tools except denied",
			restriction: &toolregistry.ToolRestriction{
				Deny: []string{"write"},
			},
			want: []string{"read"},
		},
		{
			name: "empty allow admits no inherited tools",
			restriction: &toolregistry.ToolRestriction{
				Allow: []string{},
			},
			want: nil,
		},
		{
			name: "allow and deny intersect",
			restriction: &toolregistry.ToolRestriction{
				Allow: []string{"read", "write"},
				Deny:  []string{"read"},
			},
			want: []string{"write"},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			merged, err := Merge(toolregistry.Configuration{
				Global: toolregistry.Catalog{
					Schemas: []llm.ToolSchema{
						testSchema("read"),
						testSchema("write"),
					},
				},
				Custom: toolregistry.Catalog{
					Restriction: testCase.restriction,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(merged))
			for _, entry := range merged {
				got = append(got, entry.Schema.Name)
			}
			want := slices.Clone(testCase.want)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("tools = %v, want %v", got, testCase.want)
			}
		})
	}
}
