package deepseek

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/attachment/image"
	"github.com/gorenx/goren/llm"
	"strconv"
	"strings"
)

// imageEncoder owns the per-request image budget and transient wire assembly.
type imageEncoder struct {
	storage ImageReader
	count   int
	bytes   int64
}

func (assembly *imageEncoder) encodeParts(ctx context.Context, content []agentmessage.ContentBlock) ([]wireContentPart, error) {
	var parts []wireContentPart
	for _, entry := range content {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var ref *image.ImageRef
		switch block := entry.(type) {
		case agentmessage.ImageBlock:
			ref = &block.Attachment
		case *agentmessage.ImageBlock:
			if block != nil {
				ref = &block.Attachment
			}
		}
		if ref != nil {
			assembly.count++
			if assembly.count > 600 || ref.Bytes <= 0 || ref.Bytes > 32<<20 || assembly.bytes+int64(base64.StdEncoding.EncodedLen(int(ref.Bytes))) > 40<<20 {
				return nil, llm.MustLlmError("Images exceed the DeepSeek request limit; start a new conversation or send fewer images.", "INVALID_REQUEST")
			}
			picture, err := assembly.storage.ReadImage(ctx, ref.Clone())
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, llm.MustLlmError("Unable to read an image for DeepSeek.", "INVALID_REQUEST")
			}
			if int64(len(picture.Data)) != ref.Bytes || picture.Ref.AttachmentID != ref.AttachmentID || picture.Ref.MediaType != ref.MediaType {
				return nil, llm.MustLlmError("Stored image does not match its reference.", "INVALID_REQUEST")
			}
			encoded := base64.StdEncoding.EncodeToString(picture.Data)
			assembly.bytes += int64(len(encoded))
			label := string(ref.AttachmentID)
			if ref.Name != "" {
				label = strconv.Quote(ref.Name) + " (" + label + ")"
			}
			prefix := ""
			if len(parts) > 0 {
				prefix = "\n"
			}
			parts = append(parts, wireTextPart{
				Type: "text",
				Text: fmt.Sprintf("%sImage %s; request preview %dx%dpx. It may be resized or re-encoded; source dimensions, format, and byte size may differ.", prefix, label, ref.Width, ref.Height),
			}, wireImagePart{
				Type: "image_url",
				ImageURL: wireImageURL{
					URL: "data:" + string(ref.MediaType) + ";base64," + encoded,
				},
			})
		} else if result, found := toolResultValue(entry); found {
			nested, err := assembly.encodeParts(ctx, result.Content)
			if err != nil {
				return nil, err
			}
			parts = append(parts, nested...)
		} else if entry != nil && entry.ContentType() == "text" {
			parts = append(parts, wireTextPart{
				Type: "text",
				Text: flattenText([]agentmessage.ContentBlock{entry}),
			})
		}
	}
	return parts, nil
}

func (assembly *imageEncoder) encodeMessages(ctx context.Context, conversation []agentmessage.Message) ([]wireMessage, error) {
	var output []wireMessage
	var pending []wireContentPart
	flush := func() {
		if len(pending) == 0 {
			return
		}
		output = append(output, wireVisualMessage{
			Role: "user",
			Content: append([]wireContentPart{wireTextPart{
				Type: "text",
				Text: "Attached image(s) from tool result:",
			}}, pending...),
		})
		pending = nil
	}
	for _, entry := range conversation {
		if entry == nil {
			return nil, fmt.Errorf("llm-deepseek: nil message")
		}
		content := entry.ContentValue()
		if entry.ConversationRole() != agentmessage.RoleUser {
			flush()
			messages, err := SerializeMessages([]agentmessage.Message{entry})
			if err != nil {
				return nil, err
			}
			output = append(output, messages...)
			continue
		}
		var regular []agentmessage.ContentBlock
		var results []agentmessage.ToolResultBlock
		for _, block := range content {
			if result, ok := toolResultValue(block); ok {
				results = append(results, result)
			} else {
				regular = append(regular, block)
			}
		}
		parts, err := assembly.encodeParts(ctx, regular)
		if err != nil {
			return nil, err
		}
		if len(parts) > 0 || len(results) == 0 {
			flush()
			output = append(output, userParts(parts))
		}
		for _, result := range results {
			parts, err := assembly.encodeParts(ctx, result.Content)
			if err != nil {
				return nil, err
			}
			var text strings.Builder
			for _, item := range parts {
				if readable, ok := item.(wireTextPart); ok {
					text.WriteString(readable.Text)
				} else {
					pending = append(pending, item)
				}
			}
			value := text.String()
			if value == "" {
				value = emptyToolOutput
			}
			output = append(output, wireToolMessage{
				Role:       "tool",
				ToolCallID: string(result.ToolCallID),
				Content:    value,
			})
		}
	}
	flush()
	return output, nil
}

func userParts(parts []wireContentPart) wireMessage {
	var text strings.Builder
	for _, item := range parts {
		if readable, ok := item.(wireTextPart); ok {
			text.WriteString(readable.Text)
		} else {
			return wireVisualMessage{
				Role:    "user",
				Content: parts,
			}
		}
	}
	return wireUserMessage{
		Role:    "user",
		Content: text.String(),
	}
}
