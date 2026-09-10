package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/gorenx/goren/attachment"
)

// Cache limits bound disposable I/O, never successful transform output.
const cacheByteLimit = 64 << 20

func (service *Store) readCache(ctx context.Context, id ImageVariantID, source Image, policy ImageRequestPolicy, original Metadata) (Variant, bool) {
	data, err := service.objects.ReadCache(ctx, string(id), cacheByteLimit)
	if err != nil || len(data) == 0 || len(data) > cacheByteLimit {
		return Variant{}, false
	}
	facts, err := service.normalizer.engine.Probe(ctx, data)
	if err != nil {
		return Variant{}, false
	}
	maximum, err := RequestImageDimensions(source.Ref.Width, source.Ref.Height, policy.MaxPixels)
	if err != nil {
		return Variant{}, false
	}
	allowed := facts.MediaType == ImagePNG || facts.MediaType == ImageJPEG || facts.MediaType == ImageWebP
	alphaOK := facts.HasAlpha == original.HasAlpha || (original.HasAlpha && !facts.HasAlpha && facts.MediaType == ImageWebP)
	if !allowed || !alphaOK || facts.Width <= 0 || facts.Height <= 0 || facts.Width > maximum.Width || facts.Height > maximum.Height || facts.Depth != "uchar" || facts.Space != "srgb" || facts.Animated || facts.CarriesMetadata {
		return Variant{}, false
	}
	if err := service.normalizer.engine.Decode(ctx, data); err != nil {
		return Variant{}, false
	}
	if ctx.Err() != nil {
		return Variant{}, false
	}
	digest := sha256.Sum256(data)
	return Variant{
		VariantID: id,
		Source:    source.Ref.Clone(),
		Image: Image{
			Ref: ImageRef{
				AttachmentID: attachment.AttachmentID("sha256:" + hex.EncodeToString(digest[:])),
				MediaType:    facts.MediaType,
				Width:        facts.Width,
				Height:       facts.Height,
				Bytes:        int64(len(data)),
				Name:         source.Ref.Name,
			},
			Data: data,
		},
		Depth:    facts.Depth,
		Space:    facts.Space,
		HasAlpha: facts.HasAlpha,
	}, true
}
