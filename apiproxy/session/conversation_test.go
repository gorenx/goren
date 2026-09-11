package sessionapi

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"

	"github.com/gorenx/goren/agentmessage"
)

func TestReplaceMessageContentPreservesLooseTextExtension(t *testing.T) {
	t.Parallel()
	messageValue, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("before")},
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := replaceMessageContent(messageValue, []json.RawMessage{
		json.RawMessage(`{"type":"text","text":"after","extension":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(replaced)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(encoded) || !reflect.DeepEqual(replaced.SourceValue(), messageValue.SourceValue()) ||
		!bytes.Contains(encoded, []byte(`"extension":1`)) {
		t.Fatalf("replaced message = %s", encoded)
	}
	readable, supported := replaced.ContentValue()[0].(agentmessage.PlainTextContent)
	if !supported {
		t.Fatalf("extended text type = %T, want PlainTextContent", replaced.ContentValue()[0])
	}
	text, available := readable.PlainText()
	if !available || text != "after" {
		t.Fatalf("extended text = %q, available = %t", text, available)
	}
}

func TestLiveControlsDoNotRecoverColdSessions(t *testing.T) {
	methods := rpc.NewCatalog()
	directory := &activationDirectory{}
	flow := &sessionConversation{
		access: &sessionAccess{
			runtimeSessions: &activation{
				agents: directory,
			},
		},
	}
	// No history port is supplied: any accidental recovery is a test failure.
	if err := rpc.RegisterUnary(methods, UpdateQueueMethod, DecodeUpdateQueueRequest, flow.UpdateQueue); err != nil {
		t.Fatal(err)
	}
	if err := rpc.RegisterUnary(methods, CancelMethod, DecodeCancelRequest, flow.Cancel); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		method  string
		payload string
		code    connection.RPCErrorCode
	}{
		{
			method:  UpdateQueueMethod,
			payload: `{"sessionId":"cold","itemId":"input","action":{"kind":"remove"}}`,
			code:    connection.ErrorQueueItemNotFound,
		},
		{
			method:  CancelMethod,
			payload: `{"sessionId":"cold"}`,
			code:    connection.ErrorSessionNotFound,
		},
		{
			method:  UpdateQueueMethod,
			payload: `{"sessionId":"cold","itemId":"input","action":{"kind":"edit","content":[{"type":"image"}]}}`,
			code:    connection.ErrorAttachment,
		},
	}
	for _, scenario := range cases {
		result, err := methods.DispatchUnary(context.Background(), scenario.method, "request", json.RawMessage(scenario.payload))
		if err != nil || result.Error == nil || result.Error.Code != scenario.code {
			t.Fatalf("%s: %+v %v", scenario.method, result, err)
		}
	}
	if directory.opening.ID != "" {
		t.Fatal("live control created an Agent")
	}
	if directory.resolves != 2 {
		t.Fatalf("invalid edit should be rejected before resolution: %d", directory.resolves)
	}
}
