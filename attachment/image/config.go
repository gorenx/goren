package image

import (
	"fmt"
	"slices"

	"github.com/gorenx/goren/attachment"
)

// Config is the resolved, owner-defined image policy. Decode external overrides
// into DefaultConfig, reject unknown fields, then Validate before construction.
type Config struct {
	MaxImageBytes               int64 `json:"maxImageBytes"`
	MaxImagesPerMessage         int64 `json:"maxImagesPerMessage"`
	MaxMessageImageBytes        int64 `json:"maxMessageImageBytes"`
	MaxImagePixels              int64 `json:"maxImagePixels"`
	MaxImageDimension           int64 `json:"maxImageDimension"`
	NormalizedImageMaxPixels    int64 `json:"normalizedImageMaxPixels"`
	NormalizedImageMaxDimension int64 `json:"normalizedImageMaxDimension"`
	NormalizedImageMaxBytes     int64 `json:"normalizedImageMaxBytes"`
	ImageCompressionConcurrency int   `json:"imageCompressionConcurrency"`
}

// DefaultConfig supplies the image policy from the pinned image-specific baseline.
func DefaultConfig() Config {
	return Config{
		MaxImageBytes:               20 << 20,
		MaxImagesPerMessage:         20,
		MaxMessageImageBytes:        200 << 20,
		MaxImagePixels:              64_000_000,
		MaxImageDimension:           8192,
		NormalizedImageMaxPixels:    2048 * 2048,
		NormalizedImageMaxDimension: 8192,
		NormalizedImageMaxBytes:     4 << 20,
		ImageCompressionConcurrency: 2,
	}
}

// Validate refuses invalid explicit configuration instead of silently defaulting.
func (settings Config) Validate() error {
	for _, field := range []struct {
		name  string
		value int64
	}{
		{
			name:  "maxImageBytes",
			value: settings.MaxImageBytes,
		},
		{
			name:  "maxImagesPerMessage",
			value: settings.MaxImagesPerMessage,
		},
		{
			name:  "maxMessageImageBytes",
			value: settings.MaxMessageImageBytes,
		},
		{
			name:  "maxImagePixels",
			value: settings.MaxImagePixels,
		},
		{
			name:  "maxImageDimension",
			value: settings.MaxImageDimension,
		},
		{
			name:  "normalizedImageMaxPixels",
			value: settings.NormalizedImageMaxPixels,
		},
		{
			name:  "normalizedImageMaxDimension",
			value: settings.NormalizedImageMaxDimension,
		},
		{
			name:  "normalizedImageMaxBytes",
			value: settings.NormalizedImageMaxBytes,
		},
	} {
		if field.value <= 0 || field.value > 1<<53-1 {
			return fmt.Errorf("attachment: %s must be a positive safe integer", field.name)
		}
	}
	if settings.ImageCompressionConcurrency < 1 || settings.ImageCompressionConcurrency > 8 {
		return fmt.Errorf("attachment: imageCompressionConcurrency must be between 1 and 8")
	}
	return nil
}

// ImageLimits is a detached snapshot of source-image admission restrictions.
type ImageLimits struct {
	MaxImageBytes        int64            `json:"maxImageBytes"`
	MaxImagesPerMessage  int64            `json:"maxImagesPerMessage"`
	MaxMessageImageBytes int64            `json:"maxMessageImageBytes"`
	MaxImagePixels       int64            `json:"maxImagePixels"`
	MaxImageDimension    int64            `json:"maxImageDimension"`
	MediaTypes           []ImageMediaType `json:"mediaTypes"`
}

// Limits projects validated deployment settings without retaining mutable state.
func (settings Config) Limits() ImageLimits {
	return ImageLimits{
		MaxImageBytes:        settings.MaxImageBytes,
		MaxImagesPerMessage:  settings.MaxImagesPerMessage,
		MaxMessageImageBytes: settings.MaxMessageImageBytes,
		MaxImagePixels:       settings.MaxImagePixels,
		MaxImageDimension:    settings.MaxImageDimension,
		MediaTypes:           []ImageMediaType{ImagePNG, ImageJPEG, ImageWebP, ImageGIF},
	}
}

// ValidateBatch preserves Harness's count, aggregate bytes, then MIME ordering.
// Individual image decode and limits run during preparation, after this check.
func (bounds ImageLimits) ValidateBatch(inputs []ImageInput) error {
	if int64(len(inputs)) > bounds.MaxImagesPerMessage {
		return &attachment.Error{
			Code:    TooManyImages,
			Message: "Image batch exceeds the configured image-count limit.",
		}
	}
	remaining := bounds.MaxMessageImageBytes
	for _, input := range inputs {
		if int64(len(input.Data)) > remaining {
			return &attachment.Error{
				Code:    ImagesTooLarge,
				Message: "Image batch exceeds the configured aggregate image-byte limit.",
			}
		}
		remaining -= int64(len(input.Data))
	}
	for _, input := range inputs {
		if !slices.Contains(bounds.MediaTypes, input.MediaType) {
			return &attachment.Error{
				Code:    UnsupportedImageType,
				Message: "Image type is not accepted by this deployment.",
			}
		}
	}
	return nil
}
