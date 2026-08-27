package agentmessage

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/attachment"
	"github.com/gorenx/goren/internal/jsonvalue"
)

// CallID correlates one model-issued tool call with its result.
type CallID string

// ContentBlock is the provider-neutral model-content extension contract.
// Implementations expose a stable discriminant and return a detached clone.
type ContentBlock interface {
	ContentType() string
	CloneContent() (ContentBlock, error)
}

// PlainTextContent is the capability adapters use for visible text without
// depending on one concrete block representation.
type PlainTextContent interface {
	ContentBlock
	PlainText() (string, bool)
}

// TextBlock is plain user-visible text.
type TextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// NewTextBlock creates one canonical text block.
func NewTextBlock(content string) TextBlock {
	return TextBlock{
		Type: "text",
		Text: content,
	}
}

// ContentType returns the canonical discriminant.
func (TextBlock) ContentType() string { return "text" }

// PlainText returns the block's model-visible text.
func (source TextBlock) PlainText() (string, bool) { return source.Text, true }

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

// ContentType returns the canonical discriminant.
func (ReasoningBlock) ContentType() string { return "reasoning" }

// CloneContent returns an independent value copy.
func (source ReasoningBlock) CloneContent() (ContentBlock, error) {
	source.Type = "reasoning"
	return source, nil
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
	IsError    bool           `json:"-"`

	isErrorPresent bool
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

// MarshalJSON preserves the distinction between an omitted optional isError
// field and the explicit false emitted by NewToolResultMessage.
func (source ToolResultBlock) MarshalJSON() ([]byte, error) {
	content, err := CloneContentBlocks(source.Content)
	if err != nil {
		return nil, err
	}
	wireValue := struct {
		Type       string         `json:"type"`
		ToolCallID CallID         `json:"toolCallId"`
		Content    []ContentBlock `json:"content"`
		IsError    *bool          `json:"isError,omitempty"`
	}{
		Type:       "tool-result",
		ToolCallID: source.ToolCallID,
		Content:    content,
	}
	if source.isErrorPresent || source.IsError {
		isError := source.IsError
		wireValue.IsError = &isError
	}
	return json.Marshal(wireValue)
}

// OpaqueContentBlock preserves a plugin-defined block across durable JSON
// boundaries even when the defining plugin is not currently loaded.
type OpaqueContentBlock struct {
	typeName string
	rawValue json.RawMessage
}

// NewOpaqueContentBlock validates and snapshots one extension block.
func NewOpaqueContentBlock(typeName string, rawValue json.RawMessage) (OpaqueContentBlock, error) {
	if typeName == "" {
		return OpaqueContentBlock{}, errors.New("llm: opaque content type is empty")
	}
	detached, err := jsonvalue.Clone(rawValue)
	if err != nil || !jsonvalue.IsObject(detached) {
		return OpaqueContentBlock{}, errors.New("llm: opaque content must be a lossless JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(detached, &fields); err != nil {
		return OpaqueContentBlock{}, err
	}
	var encodedType string
	if err := json.Unmarshal(fields["type"], &encodedType); err != nil || encodedType != typeName {
		return OpaqueContentBlock{}, errors.New("llm: opaque content discriminant does not match")
	}
	return OpaqueContentBlock{
		typeName: typeName,
		rawValue: detached,
	}, nil
}

// ContentType returns the retained plugin discriminant.
func (entry OpaqueContentBlock) ContentType() string { return entry.typeName }

// PlainText exposes a losslessly retained text extension to adapters while
// leaving every other opaque discriminant non-textual.
func (entry OpaqueContentBlock) PlainText() (string, bool) {
	if entry.typeName != "text" {
		return "", false
	}
	var fields struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(entry.rawValue, &fields) != nil || fields.Text == nil {
		return "", false
	}
	return *fields.Text, true
}

// CloneContent returns an independent lossless JSON snapshot.
func (entry OpaqueContentBlock) CloneContent() (ContentBlock, error) {
	return NewOpaqueContentBlock(entry.typeName, entry.rawValue)
}

// MarshalJSON returns the original extension object.
func (entry OpaqueContentBlock) MarshalJSON() ([]byte, error) {
	if entry.typeName == "" || len(entry.rawValue) == 0 {
		return nil, errors.New("llm: invalid opaque content block")
	}
	return append([]byte(nil), entry.rawValue...), nil
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

// InvalidContentBlockError reports an invalid extension implementation.
type InvalidContentBlockError struct {
	Index int
}

func (problem *InvalidContentBlockError) Error() string {
	return "llm: invalid content block"
}
