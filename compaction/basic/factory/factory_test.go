package factory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/compaction/basic"
)

func TestFactoryStrictlyConstructsValidatedBasicCompaction(t *testing.T) {
	t.Parallel()
	owner := New(basic.RuntimeOptions{})
	instance, err := owner.Create(
		context.Background(),
		json.RawMessage(`{"thresholdRatio":0.6,"retainTokens":100,"auto":false}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	metadata := instance.Manifest()
	if metadata.Name != basic.PluginName || len(metadata.Waterfalls) != 0 ||
		len(metadata.Events) != 0 {
		t.Fatalf("plugin manifest = %#v", metadata)
	}
	testCases := []struct {
		name      string
		rawValue  json.RawMessage
		wantError string
	}{
		{
			name:      "unknown field",
			rawValue:  json.RawMessage(`{"models":{}}`),
			wantError: "unknown field",
		},
		{
			name:      "duplicate field",
			rawValue:  json.RawMessage(`{"auto":true,"auto":false}`),
			wantError: "duplicate field",
		},
		{
			name:      "fractional retry",
			rawValue:  json.RawMessage(`{"compactionRetries":1.5}`),
			wantError: "cannot unmarshal",
		},
		{
			name:      "invalid retention pair",
			rawValue:  json.RawMessage(`{"retainRatio":0.1,"retainTokens":10}`),
			wantError: "mutually exclusive",
		},
		{
			name:      "partial summary route",
			rawValue:  json.RawMessage(`{"summarizationProvider":"p"}`),
			wantError: "must be set together",
		},
		{
			name:      "duplicate route policy",
			rawValue:  json.RawMessage(`{"modelPolicies":[{"provider":"p","model":"m"},{"provider":"p","model":"m"}]}`),
			wantError: "duplicate model policy",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := owner.Create(context.Background(), testCase.rawValue)
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("Create error = %v, want %q", err, testCase.wantError)
			}
		})
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = owner.Create(cancelled, json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("cancelled Create error = %v", err)
	}
}
