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
