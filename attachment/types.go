// Package attachment owns durable attachment identities and immutable media
// metadata. Storage and upload behavior remain outside this contract slice.
package attachment

// AttachmentID is the durable opaque identity of one immutable attachment.
type AttachmentID string

// ImageMediaType is the closed raster format vocabulary accepted by the
// version-one attachment path.
type ImageMediaType string

const (
	ImagePNG  ImageMediaType = "image/png"
	ImageJPEG ImageMediaType = "image/jpeg"
	ImageWebP ImageMediaType = "image/webp"
	ImageGIF  ImageMediaType = "image/gif"
)

// ImageAttachmentRef is durable image metadata. It contains no filesystem
// path, bearer URL, or encoded image bytes.
type ImageAttachmentRef struct {
	AttachmentID AttachmentID   `json:"attachmentId"`
	MediaType    ImageMediaType `json:"mediaType"`
	Bytes        int64          `json:"bytes"`
	Width        int64          `json:"width"`
	Height       int64          `json:"height"`
	Name         string         `json:"name,omitempty"`
}
