package toolregistry_test

import (
	"context"
	"slices"
	"testing"

	toolregistry "github.com/gorenx/goren/tools/registry"
	"github.com/gorenx/goren/tools/tool"
)

type unopenedProvider struct {
	name string
}

func (*unopenedProvider) Open(context.Context) (tool.Implementation, error) {
	panic("registry must not construct tool implementations")
}

func configuredTool(name string) toolregistry.Tool {
	return toolregistry.Tool{
		Name: name,
		Provider: &unopenedProvider{
			name: name,
		},
		Visible: true,
	}
}

func TestRegistryRetainsMergedConfigurationUntilSettlement(t *testing.T) {
	var catalog toolregistry.Registry
	if _, err := catalog.Configure("initial", "agent", nil); err == nil {
		t.Fatal("missing defaults became an empty configuration")
	}
	defaults := []toolregistry.Tool{configuredTool("read"), configuredTool("ask")}
	if err := catalog.InitializeDefaults(defaults); err != nil {
		t.Fatal(err)
	}
	defaults[0].Name = "mutated"
	overrides := []toolregistry.Tool{configuredTool("ask"), configuredTool("write")}
	overrides[0].Visible = false
	expectedProvider := overrides[0].Provider
	changed, err := catalog.Configure("initial", "agent", overrides)
	if err != nil || changed.AgentID != "agent" || changed.ID != "initial" {
		t.Fatalf("configuration = %#v, %v", changed, err)
	}
	overrides[0].Name = "mutated"
	configured, found := catalog.ReadConfiguration("initial")
	if !found || !slices.Equal(configured.Update.Names, []string{"read", "ask", "write"}) || configured.Tools[1].Visible || configured.Tools[1].Provider != expectedProvider {
		t.Fatalf("merged configuration = %#v, %v", configured, found)
	}
	configured.Tools[0].Name = "mutated"
	configured.Update.Names[0] = "mutated"
	retained, _ := catalog.ReadConfiguration("initial")
	if retained.Tools[0].Name != "read" || retained.Update.Names[0] != "read" {
		t.Fatal("configuration was mutated through a read result")
	}
	if _, found := catalog.Read("agent"); found {
		t.Fatal("configuration registration published a prompt baseline")
	}
	if _, err := catalog.Publish("initial"); err == nil {
		t.Fatal("configuration registration replaced tool definition registration")
	}
	if _, err := catalog.Configure("initial", "agent", nil); err == nil {
		t.Fatal("update ID was overwritten")
	}
	if _, err := catalog.Configure("next", "agent", nil); err == nil {
		t.Fatal("unsettled Agent update was bypassed")
	}
	if err := catalog.Register("initial", candidate("ask", true)); err == nil {
		t.Fatal("candidate changed the configured hidden tool to visible")
	}
	for _, entry := range retained.Tools {
		if err := catalog.Register("initial", candidate(entry.Name, entry.Visible)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := catalog.Publish("initial"); err != nil {
		t.Fatal(err)
	}
	if _, found := catalog.ReadConfiguration("initial"); !found {
		t.Fatal("publication discarded configuration before runtime settlement")
	}
	if err := catalog.Settle("initial"); err != nil {
		t.Fatal(err)
	}
	if _, found := catalog.ReadConfiguration("initial"); found {
		t.Fatal("settled update retained provider references")
	}
}

func TestRegistryRejectsInvalidConfigurationWithoutReservingUpdate(t *testing.T) {
	var catalog toolregistry.Registry
	if err := catalog.InitializeDefaults(nil); err != nil {
		t.Fatal(err)
	}
	if err := catalog.InitializeDefaults(nil); err == nil {
		t.Fatal("defaults were initialized twice")
	}
	for _, entries := range [][]toolregistry.Tool{
		{configuredTool("")},
		{configuredTool(" read")},
		{configuredTool("run_code")},
		{{Name: "missing"}},
		{configuredTool("same"), configuredTool("same")},
	} {
		if _, err := catalog.Configure("invalid", "agent", entries); err == nil {
			t.Fatalf("invalid configuration accepted: %#v", entries)
		}
		if _, found := catalog.Lookup("invalid"); found {
			t.Fatal("invalid input reserved a batch")
		}
	}
	if _, err := catalog.Configure("empty", "agent", nil); err != nil {
		t.Fatal(err)
	}
	if input, found := catalog.ReadConfiguration("empty"); !found || len(input.Tools) != 0 {
		t.Fatalf("explicit empty configuration = %#v, %v", input, found)
	}
	if err := catalog.Abort("empty"); err != nil {
		t.Fatal(err)
	}
	if _, found := catalog.ReadConfiguration("empty"); found {
		t.Fatal("aborted configuration was retained")
	}
}
