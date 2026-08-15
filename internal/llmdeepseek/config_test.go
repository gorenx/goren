package llmdeepseek

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestResolveOptionsDefaultsAndEnvironmentPrecedence(t *testing.T) {
	t.Parallel()
	connection, err := ResolveOptions(Config{}, Environment{LookupEnv: func(name string) (string, bool) {
		if name == BaseURLEnv {
			return "https://gateway.example", true
		}
		return "", false
	}})
	if err != nil {
		t.Fatal(err)
	}
	if connection.APIKeyEnv != DefaultAPIKeyEnv || connection.BaseURL != "https://gateway.example" ||
		connection.MaxTokens != DefaultMaxTokens || connection.DefaultContextWindow != DefaultContextWindow ||
		connection.StreamIdleTimeout.Milliseconds() != DefaultStreamIdleTimeoutMS || len(connection.Models) != 2 {
		t.Fatalf("resolved defaults = %#v", connection)
	}
	explicit := "https://configured.example"
	connection, err = ResolveOptions(Config{BaseURL: &explicit}, Environment{LookupEnv: func(string) (string, bool) {
		return "https://ignored.example", true
	}})
	if err != nil || connection.BaseURL != explicit {
		t.Fatalf("explicit endpoint = (%#v, %v)", connection, err)
	}
}

func TestConfigPreservesCatalogOmissionAndRejectsInvalidShapes(t *testing.T) {
	t.Parallel()
	var settings Config
	if err := json.Unmarshal([]byte(`{"models":[{"id":"fallback"},{"id":"named","name":"Named"}]}`), &settings); err != nil {
		t.Fatal(err)
	}
	connection, err := ResolveOptions(settings, Environment{})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Models[0].Name != nil || connection.Models[1].Name == nil || *connection.Models[1].Name != "Named" {
		t.Fatalf("catalog names = %#v", connection.Models)
	}

	tests := []struct {
		label string
		raw   string
		want  string
	}{
		{label: "unknown top-level", raw: `{"extra":true}`, want: "unknown field"},
		{label: "unknown catalog", raw: `{"models":[{"id":"m","extra":true}]}`, want: "unknown field"},
		{label: "null catalog name", raw: `{"models":[{"id":"m","name":null}]}`, want: "must not be null"},
		{label: "empty catalog name", raw: `{"models":[{"id":"m","name":""}]}`, want: "empty name"},
		{label: "fractional context", raw: `{"models":[{"id":"m","contextWindow":1.5}]}`, want: "invalid catalog model contextWindow"},
		{label: "duplicate model", raw: `{"models":[{"id":"m"},{"id":"m"}]}`, want: "duplicate catalog model"},
		{label: "disabled high", raw: `{"thinking":"disabled","reasoningEffort":"high"}`, want: "only reasoningEffort off"},
		{label: "unsafe max tokens", raw: `{"maxTokens":9007199254740992}`, want: "positive safe integer"},
		{label: "null retry", raw: `{"retryPolicy":null}`, want: "must not be null"},
	}
	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.label, func(t *testing.T) {
			t.Parallel()
			var candidate Config
			decodeErr := json.Unmarshal([]byte(testCase.raw), &candidate)
			if decodeErr == nil {
				_, decodeErr = ResolveOptions(candidate, Environment{})
			}
			if decodeErr == nil || !strings.Contains(decodeErr.Error(), testCase.want) {
				t.Fatalf("error = %v, want containing %q", decodeErr, testCase.want)
			}
		})
	}
}

func TestConnectionSnapshotDetachesMutableConfiguration(t *testing.T) {
	t.Parallel()
	connection, err := ResolveOptions(Config{}, Environment{})
	if err != nil {
		t.Fatal(err)
	}
	detached := connection.Snapshot()
	*detached.Models[0].Name = "changed"
	*detached.Models[0].ContextWindow = 1
	if reflect.DeepEqual(connection.Models, detached.Models) || *connection.Models[0].Name == "changed" || *connection.Models[0].ContextWindow == 1 {
		t.Fatalf("snapshot retained aliases: source=%#v detached=%#v", connection.Models, detached.Models)
	}
}
