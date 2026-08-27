package session

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/agentmessage"
)

func TestAgentMessageSurfacePayloadGolden(t *testing.T) {
	t.Parallel()
	userJSON := json.RawMessage(
		`{"id":"user-1","role":"user","content":[{"type":"text","text":"hello"}],"source":{"kind":"user"}}`,
	)
	userMessage, err := agentmessage.DecodeUserMessage(userJSON)
	if err != nil {
		t.Fatal(err)
	}
	userDraft, err := NewSurfaceEventDraft(
		UserMessageAdded,
		userMessage,
		SurfaceIntent{
			Operation: SurfaceAppend(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if userDraft.eventType != UserMessageEventName || !bytes.Equal(userDraft.data, userJSON) {
		t.Fatalf("user surface draft = (%q, %s)", userDraft.eventType, userDraft.data)
	}

	assistantJSON := json.RawMessage(
		`{"id":"assistant-1","role":"assistant","content":[{"type":"text","text":"answer"}],"source":{"kind":"model","provider":"deepseek","model":"deepseek-chat"}}`,
	)
	decodedAssistant, err := agentmessage.DecodeMessage(assistantJSON)
	if err != nil {
		t.Fatal(err)
	}
	assembledReply, specialized := decodedAssistant.(agentmessage.AssistantMessage)
	if !specialized {
		t.Fatalf("assistant type = %T", decodedAssistant)
	}
	assistantDraft, err := NewSurfaceEventDraft(
		AssistantMessaged,
		AssistantMessage{
			Turn:    1,
			Step:    2,
			Message: assembledReply,
		},
		SurfaceIntent{
			Operation: SurfaceAppend(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantAssistantData := json.RawMessage(
		`{"turn":1,"step":2,"message":{"id":"assistant-1","role":"assistant","content":[{"type":"text","text":"answer"}],"source":{"kind":"model","provider":"deepseek","model":"deepseek-chat"}}}`,
	)
	if assistantDraft.eventType != AssistantMessageEventName ||
		!bytes.Equal(assistantDraft.data, wantAssistantData) {
		t.Fatalf("assistant surface draft = (%q, %s)", assistantDraft.eventType, assistantDraft.data)
	}
}
