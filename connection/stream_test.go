package connection

import (
	"encoding/json"
	"testing"
)

func TestEncodeServerRequest(t *testing.T) {
	t.Parallel()
	event, err := NewRPCRequest("event-1", struct {
		Type      string `json:"type"`
		SessionID string `json:"sessionId"`
		LastSeq   int    `json:"lastSeq"`
	}{Type: "session/subscribed", SessionID: "session-1", LastSeq: 4})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeServerRequest(event)
	if err != nil {
		t.Fatal(err)
	}
	var message ServerRequest
	if err := json.Unmarshal(encoded, &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != ServerRequestType || message.RPCID != "event-1" || message.Method != "session/subscribed" {
		t.Fatalf("message = %#v", message)
	}
	if string(message.Payload) != `{"type":"session/subscribed","sessionId":"session-1","lastSeq":4}` {
		t.Fatalf("payload = %s", message.Payload)
	}
}

func TestEncodeServerRequestRejectsInvalidFrame(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload string
	}{
		{name: "not object", payload: `[]`},
		{name: "missing type", payload: `{}`},
		{name: "empty type", payload: `{"type":""}`},
		{name: "wrong type", payload: `{"type":1}`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, err := EncodeServerRequest(RPCRequest{RPCID: "event", Payload: json.RawMessage(testCase.payload)}); err == nil {
				t.Fatal("invalid frame was accepted")
			}
		})
	}
}
