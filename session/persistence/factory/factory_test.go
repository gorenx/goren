package factory

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/session/persistence"
)

type backgroundWriteFailureSink struct{}

func (backgroundWriteFailureSink) ReportBackgroundWriteFailure(
	persistence.BackgroundWriteFailure,
) {
}

func TestFactoryCreatesPersistencePlugin(t *testing.T) {
	t.Parallel()
	builder, err := New(backgroundWriteFailureSink{})
	if err != nil {
		t.Fatal(err)
	}
	pluginInstance, err := builder.Create(
		context.Background(),
		json.RawMessage(`{"path":"/tmp/goren-factory-session.sqlite"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if pluginInstance.Manifest().Name != persistence.PluginName {
		t.Fatalf("manifest name = %q", pluginInstance.Manifest().Name)
	}
}

func TestFactoryRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	builder, err := New(backgroundWriteFailureSink{})
	if err != nil {
		t.Fatal(err)
	}
	testCases := []struct {
		name      string
		rawConfig json.RawMessage
	}{
		{
			name:      "unknown field",
			rawConfig: json.RawMessage(`{"path":"/tmp/session.sqlite","unknown":true}`),
		},
		{
			name:      "empty path",
			rawConfig: json.RawMessage(`{"path":""}`),
		},
		{
			name:      "invalid journal",
			rawConfig: json.RawMessage(`{"path":"/tmp/session.sqlite","journalMode":"memory"}`),
		},
		{
			name: "invalid delay",
			rawConfig: json.RawMessage(
				`{"path":"/tmp/session.sqlite","writeBatchMaxDelayMs":-1}`,
			),
		},
		{
			name: "invalid cache size",
			rawConfig: json.RawMessage(
				`{"path":"/tmp/session.sqlite","preparedSessionCacheSize":-1}`,
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
		t.Fatal("New accepted a nil background write failure reporter")
	}
}
