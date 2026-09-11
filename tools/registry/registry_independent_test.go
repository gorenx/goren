package toolregistry_test

import (
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

func TestRegistryValidatesIndependentCatalogBeforeReplacingSession(t *testing.T) {
	var directory toolregistry.Registry
	input := toolregistry.Catalog{
		Independent: true,
		Schemas:     []llm.ToolSchema{schema("read", "owned")},
		ProviderIDs: map[string]tools.ProviderID{
			"read": "session-connection",
		},
	}
	if err := directory.ConfigureSession("child", input); err != nil {
		t.Fatal(err)
	}
	if err := directory.RegisterSource("global", input); err == nil {
		t.Fatal("global source accepted a Session inheritance selection")
	}
	for _, testCase := range []struct {
		name   string
		change func(*toolregistry.Catalog)
	}{
		{
			name: "inherited Provider override",
			change: func(next *toolregistry.Catalog) {
				next.Independent = false
			},
		},
		{
			name: "unowned policy",
			change: func(next *toolregistry.Catalog) {
				next.Policies = map[string]toolregistry.Policy{
					"other": {},
				}
			},
		},
		{
			name: "duplicate schema",
			change: func(next *toolregistry.Catalog) {
				next.Schemas = append(next.Schemas, next.Schemas[0])
			},
		},
		{
			name: "unowned Provider",
			change: func(next *toolregistry.Catalog) {
				next.ProviderIDs["other"] = "foreign"
			},
		},
		{
			name: "empty Provider",
			change: func(next *toolregistry.Catalog) {
				next.ProviderIDs["read"] = ""
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			next := input.Clone()
			testCase.change(&next)
			if err := directory.ConfigureSession("child", next); err == nil {
				t.Fatal("invalid change accepted")
			}
			current := directory.Read("child").Global
			if !current.Independent || len(current.Schemas) != 1 || len(current.Policies) != 0 || current.ProviderIDs["read"] != "session-connection" {
				t.Fatalf("invalid change replaced accepted selection: %+v", current)
			}
		})
	}
	if err := directory.ConfigureSession("empty", toolregistry.Catalog{
		Independent: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !directory.Read("empty").Global.Independent {
		t.Fatal("independent empty catalog was lost")
	}
}
