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
		InheritGlobal: false,
		Schemas:       []llm.ToolSchema{schema("read", "owned")},
		ProviderIDs: map[string]tools.ProviderID{
			"read": "session-connection",
		},
	}
	if err := directory.ConfigureSession("child", input); err != nil {
		t.Fatal(err)
	}
	source := input.Clone()
	source.InheritGlobal = true
	if err := directory.RegisterSource("global", source); err == nil {
		t.Fatal("global source accepted a Session inheritance selection")
	}
	for _, testCase := range []struct {
		name   string
		change func(*toolregistry.Catalog)
	}{
		{
			name: "inherited Provider override",
			change: func(next *toolregistry.Catalog) {
				next.InheritGlobal = true
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
			if current.InheritGlobal || len(current.Schemas) != 1 || len(current.Policies) != 0 || current.ProviderIDs["read"] != "session-connection" {
				t.Fatalf("invalid change replaced accepted selection: %+v", current)
			}
		})
	}
	if err := directory.ConfigureSession("empty", toolregistry.Catalog{
		InheritGlobal: false,
	}); err != nil {
		t.Fatal(err)
	}
	if directory.Read("empty").Global.InheritGlobal {
		t.Fatal("independent empty catalog was lost")
	}
}

func TestRegistryGlobalInheritanceDistinguishesAbsentAndEmptySession(t *testing.T) {
	var directory toolregistry.Registry
	if err := directory.RegisterSource("global", toolregistry.Catalog{
		Schemas: []llm.ToolSchema{schema("read", "global")},
	}); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name      string
		selection *toolregistry.Catalog
		wantTools int
	}{
		{
			name:      "unconfigured",
			wantTools: 1,
		},
		{
			name:      "explicit empty",
			selection: &toolregistry.Catalog{},
			wantTools: 0,
		},
		{
			name: "explicit inheritance",
			selection: &toolregistry.Catalog{
				InheritGlobal: true,
			},
			wantTools: 1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.selection != nil {
				if err := directory.ConfigureSession("child", *testCase.selection); err != nil {
					t.Fatal(err)
				}
			}
			if actual := len(directory.Read("child").Global.Schemas); actual != testCase.wantTools {
				t.Fatalf("tools=%d, want %d", actual, testCase.wantTools)
			}
			directory.RemoveSession("child")
			if len(directory.Read("child").Global.Schemas) != 1 {
				t.Fatal("removing Session configuration did not restore global inheritance")
			}
		})
	}
}
