package factory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/session"
)

type postCommitFailureSink struct{}

func (postCommitFailureSink) ReportPostCommitFailure(session.PostCommitFailure) {}

func TestFactoryCreatesSessionPlugin(t *testing.T) {
	t.Parallel()
	builder, err := New(postCommitFailureSink{})
	if err != nil {
		t.Fatal(err)
	}
	pluginInstance, err := builder.Create(
		context.Background(),
		json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if pluginInstance.Manifest().Name != session.PluginName {
		t.Fatalf("manifest name = %q", pluginInstance.Manifest().Name)
	}
}

func TestFactoryRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	builder, err := New(postCommitFailureSink{})
	if err != nil {
		t.Fatal(err)
	}
	testCases := []struct {
		name      string
		rawConfig json.RawMessage
	}{
		{
			name:      "unknown field",
			rawConfig: json.RawMessage(`{"unknown":true}`),
		},
		{
			name:      "non-object",
			rawConfig: json.RawMessage(`[]`),
		},
		{
			name:      "trailing value",
			rawConfig: json.RawMessage(`{} {}`),
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
		t.Fatal("New accepted a nil post-commit failure reporter")
	}
}

func TestFactoryHonorsCanceledContext(t *testing.T) {
	t.Parallel()
	builder, err := New(postCommitFailureSink{})
	if err != nil {
		t.Fatal(err)
	}
	createContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := builder.Create(
		createContext,
		json.RawMessage(`{}`),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create error = %v", err)
	}
}
