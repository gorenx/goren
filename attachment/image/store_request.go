package image

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/gorenx/goren/attachment"
)

// ImageVariantID identifies the entire transform descriptor, not source bytes.
type ImageVariantID string

// ImageRequestPolicy holds one resolved model route's pixel cap and byte target.
type ImageRequestPolicy struct {
	MaxPixels int64
	MaxBytes  int64
}

// Variant relates a request-specific image to its durable source. The Image
// carries actual request-byte metadata; Source retains the Session reference.
type Variant struct {
	VariantID ImageVariantID
	Source    ImageRef
	Image     Image
	Depth     string
	Space     string
	HasAlpha  bool
}

// job owns one shared transform and its independent waiters. Fields are guarded
// by Store.mu until done closes; published result bytes are thereafter immutable.
type job struct {
	cancel  context.CancelFunc
	done    chan struct{}
	waiters int
	result  Variant
	err     error
}

// ReadImageRequest derives one verified model-request image without modifying
// source history. It shares transformation capacity with image admission.
func (service *Store) ReadImageRequest(parent context.Context, ref ImageRef, policy ImageRequestPolicy) (Variant, error) {
	ctx, finish, err := service.begin(parent)
	if err != nil {
		return Variant{}, err
	}
	defer finish()
	variantID, err := requestVariantID(ref, policy, service.normalizer.engine.EncodingVersion())
	if err != nil {
		return Variant{}, err
	}
	source, err := service.ReadImage(ctx, ref)
	if err != nil {
		return Variant{}, err
	}
	service.mu.Lock()
	if err := ctx.Err(); err != nil {
		service.mu.Unlock()
		return Variant{}, err
	}
	work := service.pending[variantID]
	if work == nil {
		shared, settled, err := service.beginLocked(context.Background())
		if err != nil {
			service.mu.Unlock()
			return Variant{}, err
		}
		shared, cancel := context.WithCancel(shared)
		work = &job{
			cancel: cancel,
			done:   make(chan struct{}),
		}
		service.pending[variantID] = work
		go service.runRequest(shared, settled, variantID, work, source, policy)
	}
	work.waiters++
	service.mu.Unlock()
	defer service.releaseRequest(variantID, work)
	select {
	case <-ctx.Done():
		return Variant{}, ctx.Err()
	case <-work.done:
		if err := ctx.Err(); err != nil {
			return Variant{}, err
		}
		if work.err != nil {
			return Variant{}, work.err
		}
		result := work.result
		result.Source = source.Ref.Clone()
		result.Image.Ref = result.Image.Ref.Clone()
		result.Image.Ref.Name = source.Ref.Name
		result.Image.Data = slices.Clone(result.Image.Data)
		return result, nil
	}
}

func (service *Store) runRequest(ctx context.Context, settled func(), id ImageVariantID, work *job, source Image, policy ImageRequestPolicy) {
	defer settled()
	defer work.cancel()
	result, err := service.deriveRequest(ctx, id, source, policy)
	service.mu.Lock()
	defer service.mu.Unlock()
	work.result = result
	work.err = err
	if service.pending[id] == work {
		delete(service.pending, id)
	}
	close(work.done)
}

func (service *Store) releaseRequest(id ImageVariantID, work *job) {
	service.mu.Lock()
	defer service.mu.Unlock()
	work.waiters--
	if work.waiters == 0 {
		work.cancel()
		if service.pending[id] == work {
			delete(service.pending, id)
		}
	}
}

func (service *Store) deriveRequest(ctx context.Context, variantID ImageVariantID, source Image, policy ImageRequestPolicy) (Variant, error) {
	ref := source.Ref
	select {
	case service.slots <- struct{}{}:
	case <-ctx.Done():
		return Variant{}, ctx.Err()
	}
	defer func() { <-service.slots }()
	facts, err := service.normalizer.engine.Probe(ctx, source.Data)
	if err != nil {
		return Variant{}, transformFailure(ctx, err)
	}
	if cached, ok := service.readCache(ctx, variantID, source, policy, facts); ok {
		return cached, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return Variant{}, err
	}
	processor, err := NewNormalizer(service.normalizer.engine, NormalizationPolicy{
		MaxPixels:    policy.MaxPixels,
		MaxDimension: max(ref.Width, ref.Height),
		MaxBytes:     policy.MaxBytes,
	})
	if err != nil {
		return Variant{}, err
	}
	output, err := processor.Normalize(ctx, source.Data, facts)
	if err != nil {
		return Variant{}, err
	}
	verified, err := service.normalizer.engine.Probe(ctx, output.Data)
	if err != nil {
		return Variant{}, transformFailure(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return Variant{}, err
	}
	if !bytes.Equal(output.Data, source.Data) && len(output.Data) <= cacheByteLimit {
		if err := service.objects.WriteCache(ctx, string(variantID), output.Data); err != nil {
			if ctx.Err() != nil {
				return Variant{}, ctx.Err()
			}
			return Variant{}, &attachment.Error{
				Code:    attachment.AttachmentWriteFailed,
				Message: "Unable to persist request image cache.",
				Cause:   err,
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Variant{}, err
	}
	digest := sha256.Sum256(output.Data)
	output.Ref.AttachmentID = attachment.AttachmentID("sha256:" + hex.EncodeToString(digest[:]))
	output.Ref.Name = ref.Name
	return Variant{
		VariantID: variantID,
		Source:    source.Ref.Clone(),
		Image:     output,
		Depth:     verified.Depth,
		Space:     verified.Space,
		HasAlpha:  verified.HasAlpha,
	}, nil
}

func requestVariantID(ref ImageRef, policy ImageRequestPolicy, engineVersion string) (ImageVariantID, error) {
	if policy.MaxPixels <= 0 || policy.MaxBytes <= 0 || policy.MaxPixels > 1<<53-1 || policy.MaxBytes > 1<<53-1 || strings.TrimSpace(engineVersion) == "" {
		return "", &attachment.Error{
			Code:    attachment.InvalidAttachmentRef,
			Message: "Image request policy and encoding version must be valid.",
		}
	}
	descriptor := struct {
		TransformVersion string                  `json:"transformVersion"`
		AttachmentID     attachment.AttachmentID `json:"attachmentId"`
		MaxPixels        int64                   `json:"maxPixels"`
		MaxBytes         int64                   `json:"maxBytes"`
		EngineVersion    string                  `json:"engineVersion"`
		Qualities        []int                   `json:"qualities"`
		WebPEffort       int                     `json:"webpEffort"`
		ColorSpace       string                  `json:"colorSpace"`
		AlphaFormat      ImageMediaType          `json:"alphaFormat"`
		OpaqueFormat     ImageMediaType          `json:"opaqueFormat"`
	}{
		TransformVersion: "goren-request-image-v1",
		AttachmentID:     ref.AttachmentID,
		MaxPixels:        policy.MaxPixels,
		MaxBytes:         policy.MaxBytes,
		EngineVersion:    engineVersion,
		Qualities:        []int{85, 75, 60},
		WebPEffort:       0,
		ColorSpace:       "srgb",
		AlphaFormat:      ImageWebP,
		OpaqueFormat:     ImageJPEG,
	}
	data, err := json.Marshal(descriptor)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return ImageVariantID("sha256:" + hex.EncodeToString(digest[:])), nil
}
