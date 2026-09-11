package session

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/llm"
)

func TestRequestHeaderSchemaSurvivesSessionEncoding(t *testing.T) {
	original := EpochHeader{
		Tools: []llm.ToolSchema{{
			Version:     7,
			Name:        "question",
			Description: "Ask <user>",
			Parameters: json.RawMessage(`{
  "type": "object",
  "description": "<user> & answer",
  "properties": {"number": {"const": 9007199254740993}}
}`),
		}},
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var restored EpochHeader
	err = json.Unmarshal(encoded, &restored)
	if err != nil {
		t.Fatal(err)
	}
	if !EpochHeaderEqual(original, restored) {
		t.Fatal("Session encoding changed the effective request header")
	}
	restored.Tools[0].Parameters = json.RawMessage(`{"type":"object","description":"<user> & answer","properties":{"number":{"const":9007199254740992}}}`)
	if EpochHeaderEqual(original, restored) {
		t.Fatal("schema comparison lost an exact integer difference")
	}
}
