package llm_test

import (
	"github.com/gorenx/goren/llm"
	"reflect"
	"testing"
)

func TestRegistryPublishesWholeSetsAndRejectsStaleRegistration(t *testing.T) {
	var catalog llm.Registry
	first, err := catalog.RegisterAdapter([]string{"first", "alias"}, &fakeAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.RegisterAdapter([]string{"occupied"}, &fakeAdapter{}); err != nil {
		t.Fatal(err)
	}
	baseline := catalog.ListProviders()
	for _, names := range [][]string{{"new", "occupied"}, {"new", "new"}, {""}} {
		if err := catalog.ReplaceAdapter(first, names); err == nil {
			t.Fatalf("accepted invalid replacement %v", names)
		}
		if !reflect.DeepEqual(baseline, catalog.ListProviders()) {
			t.Fatal("failed replacement changed routes")
		}
	}
	if !catalog.RemoveAdapter(first) || catalog.RemoveAdapter(first) {
		t.Fatal("release is not idempotent")
	}
	next, err := catalog.RegisterAdapter([]string{"first"}, &fakeAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	if first == next || catalog.RemoveAdapter(first) {
		t.Fatal("old registration affected replacement")
	}
	if err := catalog.ReplaceAdapter(first, []string{"late"}); llmErrorCode(err) != "REGISTRATION_DISPOSED" {
		t.Fatalf("stale replacement: %v", err)
	}
	if _, err := catalog.Select("first"); err != nil {
		t.Fatal(err)
	}
}

func TestDirectorySnapshotsAndAtomicReplacement(t *testing.T) {
	var catalog llm.Registry
	declared := true
	entries := []llm.ConfigurableProvider{{
		Provider:     "p",
		DisplayName:  "P",
		SettingsNS:   "settings",
		SettingsPath: []string{"models"},
		Declared:     &declared,
	}}
	first, err := catalog.RegisterConfigurableProviders(entries)
	if err != nil {
		t.Fatal(err)
	}
	entries[0].SettingsPath[0] = "mutated"
	declared = false
	snapshot := catalog.ListConfigurableProviders()
	if snapshot[0].SettingsPath[0] != "models" || !*snapshot[0].Declared {
		t.Fatal("registration retained mutable input")
	}
	snapshot[0].SettingsPath[0] = "output mutation"
	if catalog.ListConfigurableProviders()[0].SettingsPath[0] != "models" {
		t.Fatal("directory output aliases registry")
	}
	if _, err := catalog.RegisterConfigurableProviders([]llm.ConfigurableProvider{{
		Provider:    "other",
		DisplayName: "Other",
		SettingsNS:  "settings",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.ReplaceConfigurableProviders(first, []llm.ConfigurableProvider{{
		Provider:    "other",
		DisplayName: "Other",
		SettingsNS:  "settings",
	}}); llmErrorCode(err) != "DUPLICATE_DIRECTORY" {
		t.Fatalf("conflict: %v", err)
	}
	if len(catalog.ListConfigurableProviders()) != 2 {
		t.Fatal("failed replace changed directory")
	}
}
