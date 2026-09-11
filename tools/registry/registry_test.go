package toolregistry_test

import (
	"testing"

	"github.com/gorenx/goren/llm"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

func schema(name string, description string) llm.ToolSchema {
	return llm.ToolSchema{
		Name:        name,
		Description: description,
		Parameters:  []byte(`{"type":"object"}`),
	}
}

func TestRegistryKeepsSourcesAndSessionOverridesSeparate(t *testing.T) {
	var directory toolregistry.Registry
	hidden := false
	err := directory.RegisterSource("local", toolregistry.Catalog{
		Schemas: []llm.ToolSchema{schema("read", "global read")},
		Policies: map[string]toolregistry.Policy{
			"read": {Visible: &hidden},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = directory.RegisterSource("remote", toolregistry.Catalog{
		Schemas: []llm.ToolSchema{schema("search", "remote search")},
	})
	if err != nil {
		t.Fatal(err)
	}
	visible := true
	err = directory.ConfigureSession("session-1", toolregistry.Catalog{
		InheritGlobal: true,
		Schemas:       []llm.ToolSchema{schema("read", "custom read")},
		Policies: map[string]toolregistry.Policy{
			"read": {Visible: &visible},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hidden = true
	visible = false
	input := directory.Read("session-1")
	if len(input.Global.Schemas) != 2 || input.Global.Schemas[0].Description != "global read" || input.Custom.Schemas[0].Description != "custom read" {
		t.Fatalf("Registry merged source facts: %+v", input)
	}
	if *input.Global.Policies["read"].Visible || !*input.Custom.Policies["read"].Visible {
		t.Fatal("caller mutation changed stored policy")
	}
	input.Global.Schemas[0].Parameters[0] = '!'
	*input.Custom.Policies["read"].Visible = false
	fresh := directory.Read("session-1")
	if fresh.Global.Schemas[0].Parameters[0] != '{' || !*fresh.Custom.Policies["read"].Visible {
		t.Fatal("query mutated Registry")
	}
	if len(directory.Read("session-2").Custom.Schemas) != 0 {
		t.Fatal("Session customization leaked")
	}
	directory.RemoveSession("session-1")
	if len(directory.Read("session-1").Custom.Schemas) != 0 || len(directory.Read("session-1").Global.Schemas) != 2 {
		t.Fatal("custom withdrawal changed globals")
	}
	directory.RemoveSource("remote")
	if len(directory.Read("session-1").Global.Schemas) != 1 {
		t.Fatal("source was not withdrawn")
	}
}

func TestRegistryRejectsSourceConflictsAtomicallyAndVersionsEachSchema(t *testing.T) {
	var directory toolregistry.Registry
	err := directory.RegisterSource("local", toolregistry.Catalog{
		Schemas: []llm.ToolSchema{schema("read", "first"), schema("write", "first")},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = directory.RegisterSource("remote", toolregistry.Catalog{
		Schemas: []llm.ToolSchema{schema("search", "valid"), schema("read", "conflict")},
	})
	if err == nil || len(directory.Read("").Global.Schemas) != 2 {
		t.Fatal("conflicting source changed catalog")
	}
	err = directory.RegisterSource("local", toolregistry.Catalog{
		Schemas: []llm.ToolSchema{schema("read", "second"), schema("write", "first")},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := directory.Read("").Global.Schemas
	if entries[0].Version != 2 || entries[1].Version != 1 {
		t.Fatalf("versions=%+v", entries)
	}
}
