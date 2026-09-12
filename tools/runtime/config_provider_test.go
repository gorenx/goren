package toolruntime

import (
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

func TestSessionPolicyPreservesGlobalProviderIdentity(t *testing.T) {
	input := toolregistry.Configuration{
		Global: toolregistry.Catalog{
			Schemas: []llm.ToolSchema{testSchema("read")},
			ProviderIDs: map[string]tools.ProviderID{
				"read": "remote-connection",
			},
		},
		Custom: toolregistry.Catalog{
			Policies: map[string]toolregistry.Policy{
				"read": {
					Visible: new(false),
				},
			},
		},
	}
	entries, err := Merge(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 ||
		entries[0].Schema.Description != "read" || entries[0].Visible ||
		entries[0].ProviderID != "remote-connection" {
		t.Fatalf("effective=%+v", entries)
	}
	input.Custom.ProviderIDs = map[string]tools.ProviderID{
		"read": "other",
	}
	if _, err := Merge(input); err == nil {
		t.Fatal("Merge accepted Session Provider injection")
	}
}
