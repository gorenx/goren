package apiproxy

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSessionRequestDecodersMatchIncludedUnionShapes(t *testing.T) {
	t.Parallel()
	decodedPrompt, issues := DecodeSessionPromptRequest(json.RawMessage(`{
		"sessionId":"session-1","mode":"queue",
		"content":[{"type":"text","text":"hello","ignored":true}],
		"clientTimeZone":"UTC","ignored":true
	}`))
	if len(issues) != 0 {
		t.Fatalf("prompt issues = %#v", issues)
	}
	if decodedPrompt.SessionID != "session-1" || decodedPrompt.Mode != "queue" || decodedPrompt.ClientTimeZone == nil ||
		*decodedPrompt.ClientTimeZone != "UTC" || !reflect.DeepEqual(decodedPrompt.Content, []PromptContentPart{PromptTextPart{Text: "hello"}}) {
		t.Fatalf("prompt = %#v", decodedPrompt)
	}
	update, issues := DecodeSessionUpdateQueueRequest(json.RawMessage(`{
		"sessionId":"session-1","itemId":"message-1",
		"action":{"kind":"edit","content":[{"type":"text","text":"changed","extension":1}]}
	}`))
	if len(issues) != 0 {
		t.Fatalf("update issues = %#v", issues)
	}
	edit, matched := update.Action.(EditQueueAction)
	if !matched || len(edit.Content) != 1 || string(edit.Content[0]) != `{"type":"text","text":"changed","extension":1}` {
		t.Fatalf("update = %#v", update)
	}
}

func TestSessionRequestDecodersRejectNullAndInvalidCombinations(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		decode func(json.RawMessage) int
		input  string
	}{
		{
			name: "create null id",
			decode: func(rawValue json.RawMessage) int {
				_, issues := DecodeSessionCreateRequest(rawValue)
				return len(issues)
			},
			input: `{"sessionId":null}`,
		},
		{
			name: "create workspace and cwd",
			decode: func(rawValue json.RawMessage) int {
				_, issues := DecodeSessionCreateRequest(rawValue)
				return len(issues)
			},
			input: `{"workspaceId":"workspace-1","cwd":"/tmp"}`,
		},
		{
			name: "history zero messages",
			decode: func(rawValue json.RawMessage) int {
				_, issues := DecodeSessionHistoryRequest(rawValue)
				return len(issues)
			},
			input: `{"sessionId":"session-1","maxMessages":0}`,
		},
		{
			name: "edit null content",
			decode: func(rawValue json.RawMessage) int {
				_, issues := DecodeSessionUpdateQueueRequest(rawValue)
				return len(issues)
			},
			input: `{"sessionId":"session-1","itemId":"message-1","action":{"kind":"edit","content":null}}`,
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if count := testCase.decode(json.RawMessage(testCase.input)); count == 0 {
				t.Fatal("invalid request was accepted")
			}
		})
	}
}
