package factory

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/session/title"
)

type asyncFailureSink struct{}

func (asyncFailureSink) ReportAsyncFailure(title.AsyncFailure) {}

func TestFactoryCreatesTitlePlugin(t *testing.T) {
	t.Parallel()
	builder, err := New(asyncFailureSink{})
	if err != nil {
		t.Fatal(err)
	}
	pluginInstance, err := builder.Create(
		context.Background(),
		json.RawMessage(
			`{"fallbackMaxWords":5,"fallbackMaxBytes":40,"maxTitleBytes":80}`,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if pluginInstance.Manifest().Name != title.PluginName {
		t.Fatalf("manifest name = %q", pluginInstance.Manifest().Name)
	}
}

func TestFactoryRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	builder, err := New(asyncFailureSink{})
	if err != nil {
		t.Fatal(err)
	}
	testCases := []struct {
		name      string
		rawConfig json.RawMessage
	}{
		{
			name: "unknown field",
			rawConfig: json.RawMessage(
				`{"fallbackMaxWords":5,"fallbackMaxBytes":40,"maxTitleBytes":80,"unknown":true}`,
			),
		},
		{
			name: "invalid cap",
			rawConfig: json.RawMessage(
				`{"fallbackMaxWords":5,"fallbackMaxBytes":81,"maxTitleBytes":80}`,
			),
		},
		{
			name: "incomplete route",
			rawConfig: json.RawMessage(
				`{"fallbackMaxWords":5,"fallbackMaxBytes":40,"maxTitleBytes":80,"llm":{"automaticMode":"all-prompts","targetWords":5,"targetCjkCharacters":10,"maxInputBytes":4096,"maxOutputTokens":64,"timeoutMs":60000,"provider":"p"}}`,
			),
		},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(testingContext *testing.T) {
			testingContext.Parallel()
			if _, err := builder.Create(
				context.Background(),
				testCase.rawConfig,
			); err == nil {
				testingContext.Fatal("Create accepted invalid configuration")
			}
		})
	}
}

func TestNewRequiresFailureReporter(t *testing.T) {
	t.Parallel()
	if _, err := New(nil); err == nil {
		t.Fatal("New accepted a nil asynchronous failure reporter")
	}
}
