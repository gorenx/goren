package agentmessage_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
)

func TestMessageConstructionDetachesAndRoundTrips(t *testing.T) {
	t.Parallel()
	textBlock := agentmessage.NewTextBlock("original")
	input := agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{textBlock},
		Source:  agentmessage.PluginMessageSource{Plugin: "test"},
	}
	entry, err := agentmessage.NewUserMessage(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Content[0] = agentmessage.NewTextBlock("mutated")
	if entry.StableID() == "" || entry.ConversationRole() != agentmessage.RoleUser {
		t.Fatalf("message identity/role = (%q, %q)", entry.StableID(), entry.ConversationRole())
	}
	if got := entry.ContentValue()[0].(agentmessage.TextBlock).Text; got != "original" {
		t.Fatalf("detached text = %q", got)
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := agentmessage.DecodeMessage(encoded)
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
	assistantEntry, err := agentmessage.NewAssistantMessage(agentmessage.AssistantMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("answer")},
		Source: agentmessage.ModelMessageSource{
			Provider:    "test-provider",
			Model:       "test-model",
			ReplayState: replayBytes,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	replayBytes[2] = 'X'
	modelOrigin := assistantEntry.SourceValue().(agentmessage.ModelMessageSource)
	if string(modelOrigin.ReplayState) != `{"request":1}` {
		t.Fatalf("replayState = %s", modelOrigin.ReplayState)
	}
	resultEntry, err := agentmessage.NewToolResultMessage(agentmessage.ToolResultMessageInput{
		CallID: "call-1",
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock("result"),
		},
		IsError: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	toolOrigin := resultEntry.SourceValue().(agentmessage.ToolMessageSource)
	resultBlock := resultEntry.ContentValue()[0].(agentmessage.ToolResultBlock)
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

func TestReplaceSourcePreservesAssistantIdentityAndSpecialization(t *testing.T) {
	t.Parallel()
	entry, err := agentmessage.NewAssistantMessage(agentmessage.AssistantMessageInput{
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock("answer"),
		},
		Source: agentmessage.ModelMessageSource{
			Provider:    "provider-a",
			Model:       "model-a",
			ReplayState: json.RawMessage(`{"request":1}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := agentmessage.ReplaceSource(entry, agentmessage.ModelMessageSource{
		Provider: "provider-b",
		Model:    "model-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	typedMessage, specialized := replaced.(agentmessage.AssistantMessage)
	if !specialized {
		t.Fatalf("replacement type = %T, want agentmessage.AssistantMessage", replaced)
	}
	if typedMessage.StableID() != entry.StableID() {
		t.Fatalf("replacement id = %q, want %q", typedMessage.StableID(), entry.StableID())
	}
	modelOrigin := typedMessage.SourceValue().(agentmessage.ModelMessageSource)
	if modelOrigin.Provider != "provider-b" || modelOrigin.Model != "model-b" || len(modelOrigin.ReplayState) != 0 {
		t.Fatalf("replacement source = %#v", modelOrigin)
	}
}

func TestMessageRoundTripPreservesUnknownExtensions(t *testing.T) {
	t.Parallel()
	rawValue := json.RawMessage(`{"id":"message-1","role":"user","content":[{"type":"vendor-card","value":{"x":1}}],"source":{"kind":"vendor","trace":[1,2]}}`)
	entry, err := agentmessage.DecodeMessage(rawValue)
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
	restored, err := agentmessage.DecodeUserMessage(rawValue)
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

func TestDecodeMessagePreservesExtendedPluginSource(t *testing.T) {
	t.Parallel()
	rawValue := json.RawMessage(`{"id":"message-1","role":"user","content":[{"type":"text","text":"checkpoint"}],"source":{"kind":"plugin","plugin":"compact","compactionId":"compact-1"}}`)
	restored, err := agentmessage.DecodeUserMessage(rawValue)
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
		t.Fatalf("extended plugin source round trip = %s, want %s", encoded, rawValue)
	}
}

func TestMessageDecodeRejectsUnknownCoreFields(t *testing.T) {
	t.Parallel()
	_, err := agentmessage.DecodeMessage(json.RawMessage(`{"id":"m","role":"user","content":[],"source":{"kind":"user"},"extra":true}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("decode error = %v", err)
	}
}

func TestMessageDecodeDoesNotHideMalformedPluginCoreForm(t *testing.T) {
	t.Parallel()
	_, err := agentmessage.DecodeMessage(json.RawMessage(`{"id":"m","role":"user","content":[],"source":{"kind":"plugin","plugin":"context","form":"snapshot"}}`))
	if err == nil || !strings.Contains(err.Error(), "snapshot context") {
		t.Fatalf("decode error = %v", err)
	}
}

func TestPluginContextRequiredFieldsRemainPresent(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		label  string
		origin agentmessage.PluginMessageSource
		field  string
	}{
		{
			label: "empty snapshot",
			origin: agentmessage.PluginMessageSource{
				Plugin:   "context",
				Form:     agentmessage.ContextSnapshot,
				Sections: []agentmessage.ContextSnapshotSection{},
			},
			field: `"sections":[]`,
		},
		{
			label: "empty notice",
			origin: agentmessage.PluginMessageSource{
				Plugin:  "context",
				Form:    agentmessage.ContextNotice,
				Summary: "",
			},
			field: `"summary":""`,
		},
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
