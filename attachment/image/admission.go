package image

import "github.com/gorenx/goren/attachment"

import "encoding/base64"

// EncodedImage is a canonical base64 image supplied by an inbound adapter.
type EncodedImage struct {
	MediaType ImageMediaType
	Data      string
	Name      string
}

// ImageInput contains source file bytes, not decoded raster pixels.
type ImageInput struct {
	MediaType ImageMediaType
	Data      []byte
	Name      string
}

// DecodeImage rejects non-canonical base64 without normalizing or storing bytes.
// In particular, Strict alone is insufficient because Go accepts line breaks.
func DecodeImage(input EncodedImage) (ImageInput, error) {
	data, err := base64.StdEncoding.Strict().DecodeString(input.Data)
	if err != nil || len(data) == 0 || base64.StdEncoding.EncodeToString(data) != input.Data {
		return ImageInput{}, &attachment.Error{
			Code:    InvalidImageBase64,
			Message: "Image upload is not canonical base64.",
		}
	}
	return ImageInput{
		MediaType: input.MediaType,
		Data:      data,
		Name:      input.Name,
	}, nil
}
