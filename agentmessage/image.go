package agentmessage

import "github.com/gorenx/goren/attachment/image"

// ImageBlock is one durable raster image reference.
type ImageBlock struct {
	Type       string         `json:"type"`
	Attachment image.ImageRef `json:"attachment"`
}

// ContentType returns the canonical discriminant.
func (ImageBlock) ContentType() string { return "image" }

// CloneContent returns an independent value copy.
func (source ImageBlock) CloneContent() (ContentBlock, error) {
	source.Type = "image"
	source.Attachment = source.Attachment.Clone()
	return source, nil
}

// ContentHasImage reports whether any direct or nested tool-result block is an image.
func ContentHasImage(content []ContentBlock) bool {
	for _, entry := range content {
		switch block := entry.(type) {
		case ImageBlock:
			return true
		case *ImageBlock:
			return true
		case ToolResultBlock:
			if ContentHasImage(block.Content) {
				return true
			}
		case *ToolResultBlock:
			if block != nil && ContentHasImage(block.Content) {
				return true
			}
		}
	}
	return false
}
