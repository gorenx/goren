package agentmessage_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/attachment"
)

type panicContentBlock struct{}

func (panicContentBlock) ContentType() string { panic("content failure") }
func (panicContentBlock) CloneContent() (agentmessage.ContentBlock, error) {
	return panicContentBlock{}, nil
}

func TestContentBlockCloneCoversPinnedCoreVariants(t *testing.T) {
	t.Parallel()
	source := []agentmessage.ContentBlock{
		agentmessage.TextBlock{
			Text: "visible",
		},
		agentmessage.ReasoningBlock{
			Text: "thinking",
		},
		agentmessage.ImageBlock{
			Attachment: attachment.ImageAttachmentRef{
				AttachmentID: "image-1",
				MediaType:    attachment.ImagePNG,
				Bytes:        10,
				Width:        2,
				Height:       3,
			},
		},
		agentmessage.ToolCallBlock{
			ID:        "call-1",
			Name:      "lookup",
			Arguments: `{}`,
		},
		agentmessage.ToolResultBlock{
			ToolCallID: "call-1",
			Content: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock("done"),
			},
		},
	}
	detached, err := agentmessage.CloneContentBlocks(source)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(detached)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"type":"text","text":"visible"},{"type":"reasoning","text":"thinking"},{"type":"image","attachment":{"attachmentId":"image-1","mediaType":"image/png","bytes":10,"width":2,"height":3}},{"type":"tool-call","id":"call-1","name":"lookup","arguments":"{}"},{"type":"tool-result","toolCallId":"call-1","content":[{"type":"text","text":"done"}]}]`
	if string(encoded) != want {
		t.Fatalf("content blocks = %s", encoded)
	}
}

func TestContentBlockCloneContainsExtensionPanic(t *testing.T) {
	t.Parallel()
	if _, err := agentmessage.CloneContentBlocks([]agentmessage.ContentBlock{panicContentBlock{}}); err == nil ||
		!strings.Contains(err.Error(), "content block 0 panicked") {
		t.Fatalf("clone error = %v", err)
	}
}

func TestKnownContentExtensionRoundTripsAndRetainsTextCapability(t *testing.T) {
	t.Parallel()
	rawValue := json.RawMessage(`[{"type":"text","text":"visible","extension":1}]`)
	blocks, err := agentmessage.DecodeContentBlocks(rawValue)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("block count = %d, want 1", len(blocks))
	}
	readable, supported := blocks[0].(agentmessage.PlainTextContent)
	if !supported {
		t.Fatalf("extended text type = %T, want PlainTextContent", blocks[0])
	}
	text, available := readable.PlainText()
	if !available || text != "visible" {
		t.Fatalf("plain text = %q, available = %t", text, available)
	}
	encoded, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(rawValue) {
		t.Fatalf("extension round trip = %s, want %s", encoded, rawValue)
	}
}
