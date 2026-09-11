package sessionevents

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

func TestProjectQueueFoldsDurableSplicesAndUserPlacement(t *testing.T) {
	t.Parallel()
	origin, err := agentmessage.NewOpaqueMessageSource("user", json.RawMessage(`{"kind":"user","rpcId":"rpc-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	messageValue, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("queued")},
		Source:  origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	mutation := agent.InboxSplice{
		Target:   agent.NextStep,
		Start:    0,
		Inserted: []agentmessage.UserMessage{messageValue},
	}
	encoded, err := json.Marshal(mutation)
	if err != nil {
		t.Fatal(err)
	}
	items, err := projectQueue(session.Header{}, []session.Event{{
		Type: "agent/inbox/spliced",
		Seq:  0,
		Time: 1,
		Data: encoded,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Placement != QueueSteering || items[0].ID != MessageID(messageValue.StableID()) {
		t.Fatalf("items = %#v", items)
	}
}
