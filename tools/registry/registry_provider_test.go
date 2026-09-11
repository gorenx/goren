package toolregistry_test

import (
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

func TestRegistryOwnsProviderIDsWithoutChangingSchemaVersions(t *testing.T) {
	var directory toolregistry.Registry
	input := toolregistry.Catalog{
		Schemas: []llm.ToolSchema{schema("read", "read")},
		ProviderIDs: map[string]tools.ProviderID{
			"read": "first",
		},
	}
	if err := directory.RegisterSource("remote", input); err != nil {
		t.Fatal(err)
	}
	input.ProviderIDs["read"] = "second"
	before := directory.Read("agent")
	if before.Global.ProviderIDs["read"] != "first" {
		t.Fatal("caller changed stored binding")
	}
	before.Global.ProviderIDs["read"] = "changed"
	if directory.Read("agent").Global.ProviderIDs["read"] != "first" {
		t.Fatal("query changed stored binding")
	}
	if err := directory.RegisterSource("remote", input); err != nil {
		t.Fatal(err)
	}
	after := directory.Read("agent")
	if after.Global.ProviderIDs["read"] != "second" || after.Global.Schemas[0].Version != before.Global.Schemas[0].Version {
		t.Fatal("binding did not change independently from schema")
	}
	if err := directory.ConfigureSession("agent", input); err == nil {
		t.Fatal("Session selected a Provider")
	}
	for _, invalid := range []map[string]tools.ProviderID{
		{"read": ""},
		{"unknown": "foreign"},
		{"read": " invalid "},
	} {
		input.ProviderIDs = invalid
		if err := directory.RegisterSource("remote", input); err == nil {
			t.Fatalf("accepted invalid binding: %+v", invalid)
		}
		if directory.Read("agent").Global.ProviderIDs["read"] != "second" {
			t.Fatal("invalid publication replaced existing binding")
		}
	}
	directory.RemoveSource("remote")
	if len(directory.Read("agent").Global.ProviderIDs) != 0 {
		t.Fatal("withdrawal retained Provider identities")
	}
}
