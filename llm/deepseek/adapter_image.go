package deepseek

import (
	"context"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/attachment/image"
	"github.com/gorenx/goren/llm"
)

// VisionModelID is the DeepSeek route that accepts text and image input.
const VisionModelID = "deepseek-v4-flash-vision-exp"

// ImageReader resolves immutable Session references for a transient model request.
// The adapter borrows this capability and never persists provider representations.
type ImageReader interface {
	ReadImage(ctx context.Context, ref image.ImageRef) (image.Image, error)
}

func (backend *Adapter) serializeRequest(ctx context.Context, options llm.GenerateOptions, defaults RequestDefaults) (wireRequest, error) {
	hasPictures := false
	total := 0
	edge := int64(0)
	for _, entry := range options.Messages {
		if entry != nil && agentmessage.ContentHasImage(entry.ContentValue()) {
			hasPictures = true
			if entry.ConversationRole() != agentmessage.RoleUser {
				return wireRequest{}, llm.MustLlmError("DeepSeek accepts images only in user messages.", "UNSUPPORTED_CONTENT")
			}
			count, side := imageBounds(entry.ContentValue())
			total += count
			edge = max(edge, side)
		}
	}
	if !hasPictures {
		return SerializeRequest(options, defaults)
	}
	if options.Model != VisionModelID {
		return wireRequest{}, llm.MustLlmError("Selected DeepSeek model does not support images.", "UNSUPPORTED_CONTENT")
	}
	if total > 600 || edge > 8192 || (total >= 15 && edge > 4096) {
		return wireRequest{}, llm.MustLlmError("Images exceed the DeepSeek count or dimension limit.", "INVALID_REQUEST")
	}
	if backend.pictures == nil {
		return wireRequest{}, llm.MustLlmError("Image storage is unavailable.", "UNSUPPORTED_CONTENT")
	}
	assembly := &imageEncoder{
		storage: backend.pictures,
	}
	messages, err := assembly.encodeMessages(ctx, options.Messages)
	if err != nil {
		return wireRequest{}, err
	}
	return requestWithMessages(options, defaults, messages)
}

func imageBounds(content []agentmessage.ContentBlock) (int, int64) {
	total := 0
	edge := int64(0)
	for _, entry := range content {
		switch block := entry.(type) {
		case agentmessage.ImageBlock:
			total++
			edge = max(edge, block.Attachment.Width, block.Attachment.Height)
		case *agentmessage.ImageBlock:
			if block != nil {
				total++
				edge = max(edge, block.Attachment.Width, block.Attachment.Height)
			}
		default:
			if result, ok := toolResultValue(entry); ok {
				count, side := imageBounds(result.Content)
				total += count
				edge = max(edge, side)
			}
		}
	}
	return total, edge
}
