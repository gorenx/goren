package agentloop_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentloop"
)

func TestTypedConfigDefaultsAndSourceIdentityRules(t *testing.T) {
	var settings agentloop.Config
	if err := json.Unmarshal([]byte(`{"agents":[{"id":"same"},{"id":"same"}]}`), &settings); err != nil {
		t.Fatal(err)
	}
	validated, err := agentloop.ValidateConfig(settings)
	if err != nil {
		t.Fatalf("source-compatible duplicate labels without exact identities were rejected: %v", err)
	}
	if validated.MaxParallelToolCalls() != agentloop.DefaultMaxParallelToolCalls || len(validated.ConfiguredAgents()) != 2 {
		t.Fatalf("validated config = %#v", validated)
	}

	for _, testCase := range []struct {
		label       string
		input       string
		wantMessage string
	}{
		{label: "unknown root", input: `{"unknown":true}`, wantMessage: "unknown field"},
		{label: "unknown agent", input: `{"agents":[{"id":"a","unknown":true}]}`, wantMessage: "unknown field"},
		{label: "null agent", input: `{"agents":[null]}`, wantMessage: "must be an object"},
		{label: "null optional field", input: `{"agents":[{"id":"a","provider":null}]}`, wantMessage: "provider must be a string"},
		{label: "null max tokens", input: `{"agents":[{"id":"a","maxTokens":null}]}`, wantMessage: "maxTokens must be a positive safe integer"},
		{label: "empty exact session id", input: `{"agents":[{"id":"a","sessionId":""}]}`, wantMessage: "sessionId must be non-empty"},
		{label: "invalid cap", input: `{"maxParallelToolCalls":0}`, wantMessage: "positive integer"},
		{label: "mutual identities", input: `{"agents":[{"id":"a","sessionId":"s","resumeSessionId":"r"}]}`, wantMessage: "mutually exclusive"},
		{label: "duplicate exact identity", input: `{"agents":[{"id":"a","sessionId":"s"},{"id":"b","resumeSessionId":"s"}]}`, wantMessage: "duplicate exact session identity"},
	} {
		t.Run(testCase.label, func(t *testing.T) {
			var candidate agentloop.Config
			validationErr := json.Unmarshal([]byte(testCase.input), &candidate)
			if validationErr == nil {
				_, validationErr = agentloop.ValidateConfig(candidate)
			}
			if validationErr == nil || !strings.Contains(validationErr.Error(), testCase.wantMessage) {
				t.Fatalf("validation error = %v, want containing %q", validationErr, testCase.wantMessage)
			}
		})
	}
}
