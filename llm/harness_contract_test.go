package llm_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/attachment"
	"github.com/gorenx/goren/llm"
)

type panicContentBlock struct{}

func (panicContentBlock) ContentType() string { panic("content failure") }
func (panicContentBlock) CloneContent() (llm.ContentBlock, error) {
	return panicContentBlock{}, nil
}

func TestContentBlockCloneCoversPinnedCoreVariants(t *testing.T) {
	t.Parallel()
	source := []llm.ContentBlock{
		llm.TextBlock{Text: "visible"},
		llm.ReasoningBlock{Text: "thinking"},
		llm.ImageBlock{Attachment: attachment.ImageAttachmentRef{
			AttachmentID: "image-1", MediaType: attachment.ImagePNG,
			Bytes: 10, Width: 2, Height: 3,
		}},
		llm.ToolCallBlock{ID: "call-1", Name: "lookup", Arguments: `{}`},
		llm.ToolResultBlock{ToolCallID: "call-1", Content: []llm.ContentBlock{
			llm.NewTextBlock("done"),
		}},
	}
	detached, err := llm.CloneContentBlocks(source)
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
	if _, err := llm.CloneContentBlocks([]llm.ContentBlock{panicContentBlock{}}); err == nil ||
		!strings.Contains(err.Error(), "content block 0 panicked") {
		t.Fatalf("clone error = %v", err)
	}
}
