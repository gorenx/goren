package llm

import (
	"encoding/json"
	"testing"
)

type recordingToolValidator struct {
	calls int
}

func (validationRecorder *recordingToolValidator) Validate(any) error {
	validationRecorder.calls++
	return nil
}

func TestValidateToolCallReusesOnlyMatchingPreparedSchema(t *testing.T) {
	parameterSchema := json.RawMessage(`{"type":"object"}`)
	validationRecorder := &recordingToolValidator{}
	toolDefinitions := []Tool{{
		Name:       "lookup",
		Parameters: parameterSchema,
		validator:  validationRecorder,
		validated:  string(parameterSchema),
	}}
	requestedCall := ToolCall{Name: "lookup", Arguments: json.RawMessage(`{}`)}

	if err := ValidateToolCall(toolDefinitions, requestedCall); err != nil {
		t.Fatal(err)
	}
	if validationRecorder.calls != 1 {
		t.Fatalf("prepared validator calls=%d", validationRecorder.calls)
	}

	toolDefinitions[0].Parameters = json.RawMessage(`{"type":"object","required":["q"]}`)
	if err := ValidateToolCall(toolDefinitions, requestedCall); err == nil {
		t.Fatal("expected changed schema to be recompiled and reject arguments")
	}
	if validationRecorder.calls != 1 {
		t.Fatalf("stale prepared validator was reused: calls=%d", validationRecorder.calls)
	}
}
