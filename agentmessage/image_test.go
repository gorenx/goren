package agentmessage_test

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/agentmessage"
)

// The image reference follows attachment/src/types.ts at Harness
// 5dda764ed3aa172535a7967b06ff95d9cbfe536a. Older references omit originalDimensions.
func TestImageDimensionsSurviveHistoryAndDetachFromClones(t *testing.T) {
	t.Parallel()
	for _, wire := range []string{
		`{"type":"image","attachment":{"attachmentId":"sha256:sample","mediaType":"image/jpeg","bytes":123,"width":800,"height":600}}`,
		`{"type":"image","attachment":{"attachmentId":"sha256:sample","mediaType":"image/jpeg","bytes":123,"width":800,"height":600,"originalDimensions":{"width":4000,"height":3000}}}`,
	} {
		entry, err := agentmessage.DecodeContentBlock(json.RawMessage(wire))
		if err != nil {
			t.Fatal(err)
		}
		picture, ok := entry.(agentmessage.ImageBlock)
		if !ok {
			t.Fatalf("image decoded as %T", entry)
		}
		encoded, err := json.Marshal(picture)
		if err != nil || string(encoded) != wire {
			t.Fatalf("round trip = %s, %v; want %s", encoded, err, wire)
		}
		detached, err := agentmessage.CloneContentBlocks([]agentmessage.ContentBlock{
			agentmessage.ToolResultBlock{
				ToolCallID: "read-image",
				Content:    []agentmessage.ContentBlock{picture},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if picture.Attachment.OriginalDimensions != nil {
			picture.Attachment.OriginalDimensions.Width = 1
			copyOfImage := detached[0].(agentmessage.ToolResultBlock).Content[0].(agentmessage.ImageBlock)
			if copyOfImage.Attachment.OriginalDimensions.Width != 4000 {
				t.Fatal("nested image clone retained mutable source dimensions")
			}
		}
	}
}
