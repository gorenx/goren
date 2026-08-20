package factory

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/workspace"
)

func TestFactoryCreatesWorkspacePlugin(t *testing.T) {
	t.Parallel()
	pluginInstance, err := New().Create(
		context.Background(),
		json.RawMessage(`{"path":"/tmp/goren-factory-workspace.sqlite"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if pluginInstance.Manifest().Name != workspace.PluginName {
		t.Fatalf("manifest name = %q", pluginInstance.Manifest().Name)
	}
}

func TestFactoryRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	testCases := []json.RawMessage{
		json.RawMessage(`{"path":""}`),
		json.RawMessage(`{"path":"/tmp/workspace.sqlite","journalMode":"memory"}`),
		json.RawMessage(`{"path":"/tmp/workspace.sqlite","unknown":true}`),
	}
	for _, rawConfig := range testCases {
		rawConfig := rawConfig
		t.Run(string(rawConfig), func(testingContext *testing.T) {
			testingContext.Parallel()
			if _, err := New().Create(
				context.Background(),
				rawConfig,
			); err == nil {
				testingContext.Fatal("Create accepted invalid configuration")
			}
		})
	}
}
