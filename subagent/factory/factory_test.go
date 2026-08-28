package factory_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	boundcontract "github.com/gorenx/goren/subagent/bound"
	subagentfactory "github.com/gorenx/goren/subagent/factory"
	subagentplugin "github.com/gorenx/goren/subagent/plugin"
)

func TestFactoryCreatesSubagentPlugin(t *testing.T) {
	t.Parallel()
	builder := subagentfactory.New(subagentplugin.Diagnostics{})
	rawConfig, err := json.Marshal(
		subagentfactory.Config{
			BoundDefinitions: subagentfactory.DatabaseConfig{
				Path: filepath.Join(
					t.TempDir(),
					"bound-definitions.sqlite",
				),
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	created, err := builder.Create(
		context.Background(),
		rawConfig,
	)
	if err != nil {
		t.Fatal(err)
	}
	if created.Manifest().Name != subagent.PluginName {
		t.Fatalf("Plugin name = %q", created.Manifest().Name)
	}
	// Key is a provided capability type name. Value records set membership.
	provided := make(map[string]bool)
	for _, specification := range created.Manifest().Provides {
		provided[specification.Name()] = true
	}
	for _, capability := range []plugin.ServiceType{
		plugin.ServiceOf[subagent.SeedBuilderRegistry](),
		plugin.ServiceOf[subagent.Starter](),
		plugin.ServiceOf[subagent.ChildControl](),
		plugin.ServiceOf[subagent.ExtensionRegistry](),
		plugin.ServiceOf[subagent.ChildDirectory](),
		plugin.ServiceOf[boundcontract.Definitions](),
	} {
		if !provided[capability.Name()] {
			t.Fatalf("Plugin does not provide %q", capability.Name())
		}
	}
	// Key is an event name. Value records subscription set membership.
	observed := make(map[string]bool)
	for _, subscription := range created.Manifest().Events {
		observed[subscription.Name()] = true
	}
	if !observed[session.AppendedEventName] {
		t.Fatalf("Plugin does not observe %q", session.AppendedEventName)
	}
}

func TestFactoryRejectsConfigurationAndCancelledCreation(t *testing.T) {
	t.Parallel()
	builder := subagentfactory.New(subagentplugin.Diagnostics{})
	if _, err := builder.Create(
		context.Background(),
		json.RawMessage(`{"unknown":true}`),
	); err == nil {
		t.Fatal("unknown configuration succeeded")
	}
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := builder.Create(
		cancelledContext,
		json.RawMessage(`{}`),
	); err == nil {
		t.Fatal("cancelled creation succeeded")
	}
}
