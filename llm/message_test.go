package llm_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/gorenx/goren/llm"
)

func TestMessageConstructionDetachesAndRoundTrips(t *testing.T) {
	t.Parallel()
	textBlock := llm.NewTextBlock("original")
	input := llm.UserMessageInput{
		Content: []llm.ContentBlock{textBlock},
		Source:  llm.PluginMessageSource{Plugin: "test"},
	}
	entry, err := llm.NewUserMessage(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Content[0] = llm.NewTextBlock("mutated")
	if entry.StableID() == "" || entry.ConversationRole() != llm.RoleUser {
		t.Fatalf("message identity/role = (%q, %q)", entry.StableID(), entry.ConversationRole())
	}
	if got := entry.ContentValue()[0].(llm.TextBlock).Text; got != "original" {
		t.Fatalf("detached text = %q", got)
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := llm.DecodeMessage(encoded)
	if err != nil {
		t.Fatal(err)
	}
	reencoded, err := json.Marshal(restored)
	if err != nil {
		t.Fatal(err)
	}
	if string(reencoded) != string(encoded) {
		t.Fatalf("round trip = %s, want %s", reencoded, encoded)
	}
}

func TestAssistantAndToolResultConstructionPreserveProvenance(t *testing.T) {
	t.Parallel()
	replayBytes := json.RawMessage(`{"request":1}`)
	assistantEntry, err := llm.NewAssistantMessage(llm.AssistantMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock("answer")},
		Source:  llm.ModelMessageSource{Provider: "test-provider", Model: "test-model", ReplayState: replayBytes},
	})
	if err != nil {
		t.Fatal(err)
	}
	replayBytes[2] = 'X'
	modelOrigin := assistantEntry.SourceValue().(llm.ModelMessageSource)
	if string(modelOrigin.ReplayState) != `{"request":1}` {
		t.Fatalf("replayState = %s", modelOrigin.ReplayState)
	}
	resultEntry, err := llm.NewToolResultMessage(llm.ToolResultMessageInput{
		CallID: "call-1", Content: []llm.ContentBlock{llm.NewTextBlock("result")}, IsError: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	toolOrigin := resultEntry.SourceValue().(llm.ToolMessageSource)
	resultBlock := resultEntry.ContentValue()[0].(llm.ToolResultBlock)
	if toolOrigin.CallID != "call-1" || resultBlock.ToolCallID != toolOrigin.CallID {
		t.Fatalf("tool correlation = (%q, %q)", toolOrigin.CallID, resultBlock.ToolCallID)
	}
	encodedResult, err := json.Marshal(resultEntry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encodedResult), `"isError":false`) {
		t.Fatalf("tool result constructor omitted explicit false: %s", encodedResult)
	}
}

func TestMessageRoundTripPreservesUnknownExtensions(t *testing.T) {
	t.Parallel()
	rawValue := json.RawMessage(`{"id":"message-1","role":"user","content":[{"type":"vendor-card","value":{"x":1}}],"source":{"kind":"vendor","trace":[1,2]}}`)
	entry, err := llm.DecodeMessage(rawValue)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	var gotValue map[string]json.RawMessage
	var wantValue map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawValue, &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("extension round trip = %s, want %s", encoded, rawValue)
	}
}

func TestDecodeUserMessagePreservesExtendedUserSource(t *testing.T) {
	t.Parallel()
	rawValue := json.RawMessage(`{"id":"message-1","role":"user","content":[{"type":"text","text":"hello"}],"source":{"kind":"user","rpcId":"rpc-1","clientTimeZone":"America/Los_Angeles"}}`)
	restored, err := llm.DecodeUserMessage(rawValue)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(restored)
	if err != nil {
		t.Fatal(err)
	}
	var gotValue map[string]json.RawMessage
	var wantValue map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawValue, &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("extended user source round trip = %s, want %s", encoded, rawValue)
	}
}

func TestMessageDecodeRejectsUnknownCoreFields(t *testing.T) {
	t.Parallel()
	_, err := llm.DecodeMessage(json.RawMessage(`{"id":"m","role":"user","content":[],"source":{"kind":"user"},"extra":true}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("decode error = %v", err)
	}
}

func TestStreamChunkDecodeRejectsUnknownCoreFields(t *testing.T) {
	t.Parallel()
	_, err := llm.DecodeStreamChunk(json.RawMessage(`{"type":"text-delta","index":0,"text":"x","extra":true}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("decode error = %v", err)
	}
}

func TestPluginContextRequiredFieldsRemainPresent(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		label  string
		origin llm.PluginMessageSource
		field  string
	}{
		{label: "empty snapshot", origin: llm.PluginMessageSource{Plugin: "context", Form: llm.ContextSnapshot, Sections: []llm.ContextSnapshotSection{}}, field: `"sections":[]`},
		{label: "empty notice", origin: llm.PluginMessageSource{Plugin: "context", Form: llm.ContextNotice, Summary: ""}, field: `"summary":""`},
	} {
		testCase := testCase
		t.Run(testCase.label, func(t *testing.T) {
			t.Parallel()
			encoded, err := json.Marshal(testCase.origin)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), testCase.field) {
				t.Fatalf("source = %s, want field %s", encoded, testCase.field)
			}
		})
	}
}
