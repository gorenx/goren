package modelconfig_test

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/modelconfig"
	"github.com/gorenx/goren/session"
)

func TestRestoreConfigurationKeepsSelectionAcrossLifetimes(t *testing.T) {
	owner, err := modelconfig.NewCatalog(initialDefinition())
	if err != nil {
		t.Fatal(err)
	}
	var settings llm.Configuration
	if err := owner.Restore(&settings, "blank", nil); err != nil {
		t.Fatal(err)
	}
	current, _ := settings.Get("blank")
	if current.Model != "one" {
		t.Fatalf("default: %+v", current)
	}
	encoded, err := json.Marshal(session.RequestHeaderSnapshot{
		Header: session.EpochHeader{
			Config: llm.CallConfig{
				Provider: "second",
				Model:    "logged",
			},
		},
		Reason: session.RequestHeaderInitial,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := []session.Event{{
		Seq:  0,
		Type: session.RequestHeaderEventName,
		Data: encoded,
	}}
	if err := owner.Restore(&settings, "resumed", entries); err != nil {
		t.Fatal(err)
	}
	current, _ = settings.Get("resumed")
	if current.Provider != "second" || current.Model != "logged" {
		t.Fatalf("history: %+v", current)
	}
	if err := settings.Set("resumed", llm.CallConfig{
		Provider: "first",
		Model:    "selected",
	}); err != nil {
		t.Fatal(err)
	}
	if err := owner.Restore(&settings, "resumed", entries); err != nil {
		t.Fatal(err)
	}
	current, _ = settings.Get("resumed")
	if current.Model != "selected" {
		t.Fatalf("reactivation replaced retained selection: %+v", current)
	}
}
