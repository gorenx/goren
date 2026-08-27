package sessionapi

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agentmessage"
)

func TestReplaceMessageContentPreservesLooseTextExtension(t *testing.T) {
	t.Parallel()
	messageValue, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("before")}, Source: agentmessage.UserMessageSource{},
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
