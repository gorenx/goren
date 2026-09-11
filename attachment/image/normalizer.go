package image

import (
	"context"
	"errors"
	"math"

	"github.com/gorenx/goren/attachment"
)

// Metadata describes encoded raster facts. Transform engines report oriented
// dimensions; original-byte inspectors report the stored canvas dimensions.
type Metadata struct {
	MediaType       ImageMediaType
	Width           int64
	Height          int64
	Animated        bool
	CarriesMetadata bool
	Depth           string
	Space           string
	HasAlpha        bool
}

// Encoding describes one orientation-applied, metadata-free, 8-bit sRGB encode.
// The engine selects the first frame and does not enlarge the source raster.
type Encoding struct {
	Width     int64
	Height    int64
	MediaType ImageMediaType
	Quality   int
	Effort    int
}

// Inspector validates encoded headers and the selected raster frame without transforming it.
// Probe must not allocate decoded pixels; Store checks geometry limits before Decode.
type Inspector interface {
	Probe(ctx context.Context, sourceBytes []byte) (Metadata, error)
	Decode(ctx context.Context, sourceBytes []byte) error
}

// ImageEngine extends inspection with image transforms. Implementations release
// native resources before returning and permit concurrent independent calls.
type ImageEngine interface {
	Inspector
	// EncodingVersion identifies backend and encoder versions for request caches.
	EncodingVersion() string
	// Encode returns transformed bytes and actual metadata, without publishing them.
	Encode(ctx context.Context, sourceBytes []byte, options Encoding) (Image, error)
}

// NormalizationPolicy sets provider-independent geometry limits and a byte target.
type NormalizationPolicy struct {
	MaxPixels    int64
	MaxDimension int64
	MaxBytes     int64
}

// Normalizer owns normalization decisions, independent of storage and providers.
type Normalizer struct {
	engine ImageEngine
	policy NormalizationPolicy
}

// NewNormalizer validates explicit policy and binds the selected engine.
func NewNormalizer(backend ImageEngine, policy NormalizationPolicy) (*Normalizer, error) {
	if backend == nil {
		return nil, errors.New("attachment: image engine is required")
	}
	for _, value := range []int64{policy.MaxPixels, policy.MaxDimension, policy.MaxBytes} {
		if value <= 0 || value > 1<<53-1 {
			return nil, errors.New("attachment: normalization limits must be positive safe integers")
		}
	}
	return &Normalizer{
		engine: backend,
		policy: policy,
	}, nil
}

// Normalize applies the policy to an already fully decoded source. The result
// owns its bytes, including on the byte-identical passthrough path.
func (normalizerState *Normalizer) Normalize(ctx context.Context, data []byte, facts Metadata) (Image, error) {
	if err := ctx.Err(); err != nil {
		return Image{}, err
	}
	dimensions, err := RequestImageDimensions(facts.Width, facts.Height, normalizerState.policy.MaxPixels)
	if err != nil {
		return Image{}, normalizationFailure(err)
	}
	longEdge := max(dimensions.Width, dimensions.Height)
	if longEdge > normalizerState.policy.MaxDimension {
		scale := float64(normalizerState.policy.MaxDimension) / float64(longEdge)
		dimensions.Width = max(int64(1), int64(math.Floor(float64(dimensions.Width)*scale)))
		dimensions.Height = max(int64(1), int64(math.Floor(float64(dimensions.Height)*scale)))
	}
	if facts.MediaType != ImageGIF && !facts.Animated && !facts.CarriesMetadata &&
		facts.Depth == "uchar" && facts.Space == "srgb" &&
		int64(len(data)) <= normalizerState.policy.MaxBytes &&
		dimensions.Width == facts.Width && dimensions.Height == facts.Height {
		return Image{
			Data: append([]byte(nil), data...),
			Ref: ImageRef{
				MediaType: facts.MediaType,
				Width:     facts.Width,
				Height:    facts.Height,
				Bytes:     int64(len(data)),
			},
		}, nil
	}
	mediaType := ImageJPEG
	if facts.HasAlpha {
		mediaType = ImageWebP
	}
	var smallest Image
	for _, quality := range []int{85, 75, 60} {
		if err := ctx.Err(); err != nil {
			return Image{}, err
		}
		candidate, err := normalizerState.engine.Encode(ctx, data, Encoding{
			Width:     dimensions.Width,
			Height:    dimensions.Height,
			MediaType: mediaType,
			Quality:   quality,
			Effort:    0,
		})
		if err != nil {
			return Image{}, transformFailure(ctx, err)
		}
		if smallest.Data == nil || len(candidate.Data) < len(smallest.Data) {
			smallest = candidate
		}
		if int64(len(candidate.Data)) <= normalizerState.policy.MaxBytes {
			smallest = candidate
			break
		}
	}
	if len(smallest.Data) == 0 || smallest.Ref.Width != dimensions.Width || smallest.Ref.Height != dimensions.Height || smallest.Ref.MediaType != mediaType {
		return Image{}, normalizationFailure(nil)
	}
	verified, err := normalizerState.engine.Probe(ctx, smallest.Data)
	if err != nil {
		return Image{}, transformFailure(ctx, err)
	}
	if err := normalizerState.engine.Decode(ctx, smallest.Data); err != nil {
		return Image{}, transformFailure(ctx, err)
	}
	alphaOK := facts.MediaType == ImageGIF || verified.HasAlpha == facts.HasAlpha ||
		(facts.HasAlpha && !verified.HasAlpha && verified.MediaType == ImageWebP)
	if verified.MediaType != smallest.Ref.MediaType ||
		verified.Width != smallest.Ref.Width || verified.Height != smallest.Ref.Height ||
		verified.Animated || verified.CarriesMetadata || verified.Depth != "uchar" || verified.Space != "srgb" || !alphaOK {
		return Image{}, normalizationFailure(nil)
	}
	if err := ctx.Err(); err != nil {
		return Image{}, err
	}
	smallest.Ref.Bytes = int64(len(smallest.Data))
	return smallest, nil
}

func normalizationFailure(cause error) error {
	return &attachment.Error{
		Code:    attachment.AttachmentWriteFailed,
		Message: "Image normalization did not produce a single-frame 8-bit sRGB image with matching metadata.",
		Cause:   cause,
	}
}

func transformFailure(ctx context.Context, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return normalizationFailure(cause)
}
