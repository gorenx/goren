package factory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/compaction/toolresultpruner"
)

func TestFactoryStrictlyConstructsValidatedPruner(t *testing.T) {
	t.Parallel()
	owner := New()
	instance, err := owner.Create(
		context.Background(),
		json.RawMessage(`{"thresholdChars":100,"headChars":20,"tailChars":10}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if instance.Manifest().Name != toolresultpruner.PluginName {
		t.Fatalf("plugin manifest = %#v", instance.Manifest())
	}
	testCases := []struct {
		name      string
		rawValue  json.RawMessage
		wantError string
	}{
		{
			name:      "unknown field",
			rawValue:  json.RawMessage(`{"maxChars":100}`),
			wantError: "unknown field",
		},
		{
			name:      "fractional integer",
			rawValue:  json.RawMessage(`{"headChars":1.5}`),
			wantError: "cannot unmarshal",
		},
		{
			name:      "oversized emission",
			rawValue:  json.RawMessage(`{"thresholdChars":50,"headChars":20,"tailChars":20}`),
			wantError: "exceed threshold",
		},
		{
			name:      "duplicate field",
			rawValue:  json.RawMessage(`{"headChars":1,"headChars":2}`),
			wantError: "duplicate field",
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
}
