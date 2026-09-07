package tools_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/tools"
)

func TestConfigPreservesOmissionAndRejectsUnavailablePresentation(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name        string
		input       string
		wantMessage string
	}{
		{
			name:  "defaults",
			input: `{}`,
		},
		{
			name:        "null mode",
			input:       `{"mode":null}`,
			wantMessage: "mode must be",
		},
		{
			name:        "code unavailable",
			input:       `{"mode":"code"}`,
			wantMessage: "Code Runtime bridge",
		},
		{
			name:        "both unavailable",
			input:       `{"mode":"both"}`,
			wantMessage: "Code Runtime bridge",
		},
		{
			name:        "unsupported mode",
			input:       `{"mode":"other"}`,
			wantMessage: "unsupported presentation",
		},
		{
			name:        "zero parallel limit",
			input:       `{"maxParallelSubCalls":0}`,
			wantMessage: "positive integer",
		},
		{
			name:        "null parallel limit",
			input:       `{"maxParallelSubCalls":null}`,
			wantMessage: "positive integer",
		},
		{
			name:        "unknown field",
			input:       `{"unknown":true}`,
			wantMessage: "unknown field",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var settings tools.Config
			err := json.Unmarshal([]byte(testCase.input), &settings)
			if err == nil {
				_, err = tools.ValidateConfig(settings)
			}
			if testCase.wantMessage == "" && err != nil {
				t.Fatal(err)
			}
			if testCase.wantMessage != "" &&
				(err == nil || !strings.Contains(err.Error(), testCase.wantMessage)) {
				t.Fatalf(
					"error = %v, want containing %q",
					err,
					testCase.wantMessage,
				)
			}
		})
	}
}

func TestAgentConfigOverridesValidatedDefaults(t *testing.T) {
	defaultLimit := 7
	baseline, err := tools.ValidateConfig(tools.Config{
		MaxParallelSubCalls: &defaultLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	inherited, err := baseline.WithOverrides(tools.Config{})
	if err != nil || inherited != baseline {
		t.Fatalf("omitted overrides = %#v, %v", inherited, err)
	}
	customLimit := 3
	custom, err := baseline.WithOverrides(tools.Config{
		MaxParallelSubCalls: &customLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	expected, err := tools.ValidateConfig(tools.Config{
		MaxParallelSubCalls: &customLimit,
	})
	if err != nil || custom != expected || custom == baseline {
		t.Fatalf("custom settings = %#v, %v", custom, err)
	}
	defaultLimit = 99
	customLimit = 0
	if custom != expected {
		t.Fatal("resolved configuration retained mutable input")
	}
	unchanged, err := baseline.WithOverrides(tools.Config{})
	if err != nil || unchanged != inherited {
		t.Fatal("Agent override changed the global baseline")
	}
	if _, err := baseline.WithOverrides(tools.Config{
		MaxParallelSubCalls: &customLimit,
	}); err == nil {
		t.Fatal("explicit zero silently inherited the default")
	}
	unsupported := tools.PresentationCode
	if _, err := baseline.WithOverrides(tools.Config{
		Mode: &unsupported,
	}); err == nil {
		t.Fatal("unsupported override bypassed validation")
	}
	var uninitialized tools.ValidatedConfig
	if _, err := uninitialized.WithOverrides(tools.Config{}); err == nil {
		t.Fatal("uninitialized defaults accepted")
	}
}
