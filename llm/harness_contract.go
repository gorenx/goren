package llm

import (
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/attachment"
)

// CallID correlates one model-issued tool call with its result.
type CallID string

// ContentBlock is the provider-neutral model-content extension contract.
// Implementations expose a stable discriminant and return a detached clone.
type ContentBlock interface {
	ContentType() string
	CloneContent() (ContentBlock, error)
}

// TextBlock is plain user-visible text.
type TextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// NewTextBlock creates one canonical text block.
func NewTextBlock(content string) TextBlock {
	return TextBlock{Type: "text", Text: content}
}

// ContentType returns the canonical discriminant.
func (TextBlock) ContentType() string { return "text" }

// CloneContent returns an independent value copy.
func (source TextBlock) CloneContent() (ContentBlock, error) {
	source.Type = "text"
	return source, nil
}

// ReasoningBlock retains reasoning separately from visible text.
type ReasoningBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ImageBlock is one durable raster image reference.
type ImageBlock struct {
	Type       string                        `json:"type"`
	Attachment attachment.ImageAttachmentRef `json:"attachment"`
}

// ContentType returns the canonical discriminant.
func (ImageBlock) ContentType() string { return "image" }

// CloneContent returns an independent value copy.
func (source ImageBlock) CloneContent() (ContentBlock, error) {
	source.Type = "image"
	return source, nil
}

// ContentType returns the canonical discriminant.
func (ReasoningBlock) ContentType() string { return "reasoning" }

// CloneContent returns an independent value copy.
func (source ReasoningBlock) CloneContent() (ContentBlock, error) {
	source.Type = "reasoning"
	return source, nil
}

// ToolCallBlock is one model-requested tool invocation.
type ToolCallBlock struct {
	Type      string `json:"type"`
	ID        CallID `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ContentType returns the canonical discriminant.
func (ToolCallBlock) ContentType() string { return "tool-call" }

// CloneContent returns an independent value copy.
func (source ToolCallBlock) CloneContent() (ContentBlock, error) {
	source.Type = "tool-call"
	return source, nil
}

// ToolResultBlock returns one tool invocation's model-visible result.
type ToolResultBlock struct {
	Type       string         `json:"type"`
	ToolCallID CallID         `json:"toolCallId"`
	Content    []ContentBlock `json:"content"`
	IsError    bool           `json:"isError,omitempty"`
}

// ContentType returns the canonical discriminant.
func (ToolResultBlock) ContentType() string { return "tool-result" }

// CloneContent recursively detaches nested result content.
func (source ToolResultBlock) CloneContent() (ContentBlock, error) {
	content, err := CloneContentBlocks(source.Content)
	if err != nil {
		return nil, err
	}
	source.Type = "tool-result"
	source.Content = content
	return source, nil
}

// CloneContentBlocks validates and detaches one content sequence.
func CloneContentBlocks(source []ContentBlock) (detached []ContentBlock, cloneErr error) {
	activeIndex := -1
	defer func() {
		if panicValue := recover(); panicValue != nil {
			detached = nil
			cloneErr = fmt.Errorf("llm: content block %d panicked: %v", activeIndex, panicValue)
		}
	}()
	detached = make([]ContentBlock, len(source))
	for index, entry := range source {
		activeIndex = index
		if entry == nil || entry.ContentType() == "" {
			return nil, &InvalidContentBlockError{Index: index}
		}
		cloned, err := entry.CloneContent()
		if err != nil {
			return nil, err
		}
		if cloned == nil || cloned.ContentType() != entry.ContentType() {
			return nil, &InvalidContentBlockError{Index: index}
		}
		detached[index] = cloned
	}
	return detached, nil
}

// InvalidContentBlockError reports an invalid extension implementation.
type InvalidContentBlockError struct {
	Index int
}

func (failure *InvalidContentBlockError) Error() string {
	return "llm: invalid content block"
}

// ToolSchema is the model-facing JSON Schema description of one callable
// tool. Tool registry and execution policy remain owned outside llm.
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ContextSnapshotSection is one attributed contribution to a runtime-context
// snapshot in model-visible order.
type ContextSnapshotSection struct {
	Name string `json:"name"`
	Text string `json:"text"`
}
