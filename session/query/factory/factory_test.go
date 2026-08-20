package factory

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/session/query"
)

func TestFactoryCreatesQueryPlugin(t *testing.T) {
	t.Parallel()
	pluginInstance, err := New().Create(
		context.Background(),
		json.RawMessage(`{"path":"/tmp/goren-factory-query.sqlite"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if pluginInstance.Manifest().Name != query.PluginName {
		t.Fatalf("manifest name = %q", pluginInstance.Manifest().Name)
	}
}

func TestFactoryRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		rawConfig json.RawMessage
	}{
		{
			name:      "unknown field",
			rawConfig: json.RawMessage(`{"path":"/tmp/query.sqlite","unknown":true}`),
		},
		{
			name:      "empty path",
			rawConfig: json.RawMessage(`{"path":""}`),
		},
		{
			name: "invalid limit",
			rawConfig: json.RawMessage(
				`{"path":"/tmp/query.sqlite","defaultLimit":101,"maximumLimit":100}`,
			),
		},
		{
			name: "invalid snippet",
			rawConfig: json.RawMessage(
				`{"path":"/tmp/query.sqlite","snippetCodePoints":-1}`,
			),
		},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(testingContext *testing.T) {
			testingContext.Parallel()
			if _, err := New().Create(
				context.Background(),
				testCase.rawConfig,
			); err == nil {
				testingContext.Fatal("Create accepted invalid configuration")
			}
		})
	}
}
