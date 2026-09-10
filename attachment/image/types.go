// Package image owns image references, admission, normalization and request variants.
package image

import "github.com/gorenx/goren/attachment"

// ImageMediaType is the closed raster format vocabulary accepted by the
// version-one attachment path.
type ImageMediaType string

const (
	ImagePNG  ImageMediaType = "image/png"
	ImageJPEG ImageMediaType = "image/jpeg"
	ImageWebP ImageMediaType = "image/webp"
	ImageGIF  ImageMediaType = "image/gif"
)

// ImageRef is durable image metadata. It contains no filesystem
// path, bearer URL, or encoded image bytes.
type ImageRef struct {
	AttachmentID       attachment.AttachmentID `json:"attachmentId"`
	MediaType          ImageMediaType          `json:"mediaType"`
	Bytes              int64                   `json:"bytes"`
	Width              int64                   `json:"width"`
	Height             int64                   `json:"height"`
	Name               string                  `json:"name,omitempty"`
	OriginalDimensions *ImageDimensions        `json:"originalDimensions,omitempty"`
}

// ImageDimensions records pixel dimensions after applying EXIF orientation.
type ImageDimensions struct {
	Width  int64 `json:"width"`
	Height int64 `json:"height"`
}

// Clone detaches optional metadata so immutable message snapshots share no pointers.
func (ref ImageRef) Clone() ImageRef {
	if ref.OriginalDimensions != nil {
		dimensions := *ref.OriginalDimensions
		ref.OriginalDimensions = &dimensions
	}
	return ref
}

// Image carries an image reference and its encoded bytes. Persistence is a
// method postcondition, not a property inferred from this value's type:
// preparation produces an unpublished Image; ReadImage returns a verified one.
type Image struct {
	Ref  ImageRef
	Data []byte
}
