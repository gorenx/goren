package deepseek

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/attachment"
	"github.com/gorenx/goren/attachment/image"
	"github.com/gorenx/goren/llm"
)

type pictureStorage struct{ reads int }

func (storage *pictureStorage) ReadImage(ctx context.Context, ref image.ImageRef) (image.Image, error) {
	storage.reads++
	return image.Image{
		Ref:  ref,
		Data: []byte{1},
	}, ctx.Err()
}

func TestImageSerializationMatchesHarness(t *testing.T) {
	data, err := os.ReadFile("testdata/images.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name     string `json:"name"`
			Messages []struct {
				Role    agentmessage.MessageRole `json:"role"`
				Content json.RawMessage          `json:"content"`
			} `json:"messages"`
			Wire any `json:"wire"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, sample := range fixture.Cases {
		t.Run(sample.Name, func(t *testing.T) {
			var conversation []agentmessage.Message
			for _, entry := range sample.Messages {
				blocks, err := agentmessage.DecodeContentBlocks(entry.Content)
				if err != nil {
					t.Fatal(err)
				}
				conversation = append(conversation, mustMessage(t, entry.Role, blocks))
			}
			storage := &pictureStorage{}
			backend := &Adapter{
				pictures: storage,
			}
			payload, err := backend.serializeRequest(context.Background(), llm.GenerateOptions{
				CallConfig: llm.CallConfig{
					Model: VisionModelID,
				},
				Messages: conversation,
			}, RequestDefaults{})
			if err != nil {
				t.Fatal(err)
			}
			assertJSONEqual(t, payload.Messages, sample.Wire)
		})
	}
}

func TestImageSerializationRejectsUnsupportedRolesModelsAndBudgets(t *testing.T) {
	for _, sample := range []struct {
		name    string
		role    agentmessage.MessageRole
		modelID string
		bytes   int64
	}{
		{
			name:    "text model",
			role:    agentmessage.RoleUser,
			modelID: DefaultModelID,
			bytes:   1,
		},
		{
			name:    "assistant",
			role:    agentmessage.RoleAssistant,
			modelID: VisionModelID,
			bytes:   1,
		},
		{
			name:    "oversize",
			role:    agentmessage.RoleUser,
			modelID: VisionModelID,
			bytes:   33 << 20,
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			storage := &pictureStorage{}
			backend := &Adapter{
				pictures: storage,
			}
			_, err := backend.serializeRequest(context.Background(), llm.GenerateOptions{
				CallConfig: llm.CallConfig{
					Model: sample.modelID,
				},
				Messages: []agentmessage.Message{mustMessage(t, sample.role, []agentmessage.ContentBlock{agentmessage.ImageBlock{
					Attachment: image.ImageRef{
						AttachmentID: attachment.AttachmentID("sha256:" + strings.Repeat("a", 64)),
						Bytes:        sample.bytes,
						MediaType:    image.ImagePNG,
						Width:        1,
						Height:       1,
					},
				}})},
			}, RequestDefaults{})
			if err == nil || storage.reads != 0 {
				t.Fatalf("err=%v reads=%d", err, storage.reads)
			}
		})
	}
}
