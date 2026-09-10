package imageapi

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/apiproxy/rpc"
	sessionframes "github.com/gorenx/goren/apiproxy/session/events"
	"github.com/gorenx/goren/attachment"
	"github.com/gorenx/goren/attachment/image"
	"github.com/gorenx/goren/connection"
)

// AttachmentMethod reads an image authorized by a Session log reference.
const AttachmentMethod = "session.attachment"

// AttachmentRequest selects an image through its owning Session history.
type AttachmentRequest struct {
	SessionID    sessionframes.SessionID
	AttachmentID attachment.AttachmentID
}

// AttachmentValue carries the verified reference and base64 encoded file bytes.
type AttachmentValue struct {
	Attachment image.ImageRef `json:"attachment"`
	Data       string         `json:"data"`
}

// ImageReader verifies immutable image bytes for a log-authorized reference.
// It owns no Session access policy; callers must authorize before calling it.
type ImageReader interface {
	ReadImage(ctx context.Context, ref image.ImageRef) (image.Image, error)
}

// DecodeAttachmentRequest validates the session.attachment wire fields.
func DecodeAttachmentRequest(rawPayload json.RawMessage) (AttachmentRequest, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return AttachmentRequest{}, issues
	}
	identifier, identifierIssues := rpc.RequiredString(fields, "sessionId", true)
	reference, referenceIssues := rpc.RequiredString(fields, "attachmentId", true)
	issues = append(issues, identifierIssues...)
	issues = append(issues, referenceIssues...)
	return AttachmentRequest{
		SessionID:    sessionframes.SessionID(identifier),
		AttachmentID: attachment.AttachmentID(reference),
	}, issues
}

// ImageStore admits a complete image batch and reads authorized immutable bytes.
// SaveImages returns references in input order only after every image is saved.
type ImageStore interface {
	ImageReader
	SaveImages(ctx context.Context, inputs []image.ImageInput) ([]image.ImageRef, error)
}

// Part is an encoded image in the closed session.prompt wire union.
type Part struct {
	MediaType string
	Data      string
	Name      *string
}

// PromptContentPart lets the Session owner include images without reverse imports.
func (Part) PromptContentPart() {}

// DecodePart validates image fields after the Session decoder selects the image discriminator.
func DecodePart(rawPayload json.RawMessage) (Part, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(rawPayload)
	if len(issues) != 0 {
		return Part{}, issues
	}
	mediaType, mediaIssues := rpc.RequiredString(fields, "mediaType", false)
	data, dataIssues := rpc.RequiredString(fields, "data", false)
	name, nameIssues := rpc.OptionalString(fields, "name", false)
	issues = append(issues, mediaIssues...)
	issues = append(issues, dataIssues...)
	issues = append(issues, nameIssues...)
	switch mediaType {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
	default:
		issues = append(issues, connection.ValidationIssue{
			Code:    "invalid_value",
			Path:    []string{"mediaType"},
			Message: "Invalid image media type",
		})
	}
	return Part{
		MediaType: mediaType,
		Data:      data,
		Name:      name,
	}, issues
}
