package image

import (
	"errors"

	"github.com/gorenx/goren/attachment"
)

// Image admission error codes preserve the Harness wire vocabulary.
const (
	TooManyImages          attachment.ErrorCode = "TOO_MANY_IMAGES"
	ImagesTooLarge         attachment.ErrorCode = "IMAGES_TOO_LARGE"
	UnsupportedImageType   attachment.ErrorCode = "UNSUPPORTED_IMAGE_TYPE"
	InvalidImageBase64     attachment.ErrorCode = "INVALID_IMAGE_BASE64"
	InvalidImage           attachment.ErrorCode = "INVALID_IMAGE"
	ImageTypeMismatch      attachment.ErrorCode = "IMAGE_TYPE_MISMATCH"
	ImageTooLarge          attachment.ErrorCode = "IMAGE_TOO_LARGE"
	ImageTooManyPixels     attachment.ErrorCode = "IMAGE_TOO_MANY_PIXELS"
	ImageDimensionTooLarge attachment.ErrorCode = "IMAGE_DIMENSION_TOO_LARGE"
)

// IsAdmission reports whether the caller can correct this image input failure.
func IsAdmission(err error) bool {
	var failure *attachment.Error
	if !errors.As(err, &failure) {
		return false
	}
	switch failure.Code {
	case TooManyImages, ImagesTooLarge, UnsupportedImageType, InvalidImageBase64,
		InvalidImage, ImageTypeMismatch, ImageTooLarge, ImageTooManyPixels, ImageDimensionTooLarge:
		return true
	default:
		return false
	}
}
