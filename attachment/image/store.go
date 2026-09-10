package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/gorenx/goren/attachment"
)

// ObjectStore is the image owner's durable object and disposable cache boundary.
// Implementations verify digests; they do not inspect raster or Session semantics.
type ObjectStore interface {
	// Publish verifies that data hashes to id, then durably publishes an
	// immutable object. It must not overwrite conflicting existing content.
	// Failure after publication may leave an unreferenced shared object.
	Publish(ctx context.Context, id attachment.AttachmentID, data []byte) error
	// Read bounds the read by expectedBytes and verifies the complete length
	// and digest before returning any data. Missing or corrupt objects fail.
	Read(ctx context.Context, id attachment.AttachmentID, expectedBytes int64) ([]byte, error)
	// HostPath validates id and returns the provider-owned host location.
	// An empty path means this backend has no host-file representation.
	// The result grants neither Session authorization nor tool-world access.
	HostPath(id attachment.AttachmentID) (string, error)
	// ReadCache reads at most maxBytes of verified cache payload for a transform
	// digest key. Missing, oversized or corrupt entries return an error.
	ReadCache(ctx context.Context, key string, maxBytes int64) ([]byte, error)
	// WriteCache atomically replaces a disposable entry identified by a transform
	// digest key. The key is not a digest of data or a durable attachment identity.
	WriteCache(ctx context.Context, key string, data []byte) error
}

// Store owns image admission and shared transformation capacity. It borrows
// its engine and object store; composition closes those after Store closes.
type Store struct {
	settings      Config
	normalizer    *Normalizer
	objects       ObjectStore
	slots         chan struct{}
	mu            sync.Mutex
	closing       bool
	operations    map[uint64]context.CancelFunc
	nextOperation uint64
	done          chan struct{}
	pending       map[ImageVariantID]*job
}

// NewStore validates deployment policy before constructing the image service.
func NewStore(settings Config, backend ImageEngine, storage ObjectStore) (*Store, error) {
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	if storage == nil {
		return nil, errors.New("attachment: object store is required")
	}
	processor, err := NewNormalizer(backend, NormalizationPolicy{
		MaxPixels:    settings.NormalizedImageMaxPixels,
		MaxDimension: settings.NormalizedImageMaxDimension,
		MaxBytes:     settings.NormalizedImageMaxBytes,
	})
	if err != nil {
		return nil, err
	}
	return &Store{
		settings:   settings,
		normalizer: processor,
		objects:    storage,
		slots:      make(chan struct{}, settings.ImageCompressionConcurrency),
		operations: make(map[uint64]context.CancelFunc),
		done:       make(chan struct{}),
		pending:    make(map[ImageVariantID]*job),
	}, nil
}

// Limits returns the deployment's detached source-admission restrictions.
func (service *Store) Limits() ImageLimits { return service.settings.Limits() }

func (service *Store) begin(parent context.Context) (context.Context, func(), error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.beginLocked(parent)
}

func (service *Store) beginLocked(parent context.Context) (context.Context, func(), error) {
	if service.closing {
		return nil, nil, errors.New("attachment: image service is closing")
	}
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	service.nextOperation++
	sequence := service.nextOperation
	service.operations[sequence] = cancel
	return ctx, func() {
		service.mu.Lock()
		defer service.mu.Unlock()
		cancel()
		delete(service.operations, sequence)
		if service.closing && len(service.operations) == 0 {
			close(service.done)
		}
	}, nil
}

// Close stops admission and cancels active work, then waits for actual resource
// settlement. A timeout leaves admission closed and permits another Close wait.
func (service *Store) Close(ctx context.Context) error {
	service.mu.Lock()
	if !service.closing {
		service.closing = true
		for _, cancel := range service.operations {
			cancel()
		}
		if len(service.operations) == 0 {
			close(service.done)
		}
	}
	service.mu.Unlock()
	select {
	case <-service.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SaveImage validates, normalizes and durably publishes a single source image.
func (service *Store) SaveImage(ctx context.Context, input ImageInput) (ImageRef, error) {
	refs, err := service.SaveImages(ctx, []ImageInput{input})
	if err != nil {
		return ImageRef{}, err
	}
	return refs[0], nil
}

// SaveImages prepares every member before publishing any member, preserving
// input order. Storage failure can leave unreferenced immutable objects.
func (service *Store) SaveImages(parent context.Context, inputs []ImageInput) ([]ImageRef, error) {
	ctx, finish, err := service.begin(parent)
	if err != nil {
		return nil, err
	}
	defer finish()
	if err := service.settings.Limits().ValidateBatch(inputs); err != nil {
		return nil, err
	}
	sources := slices.Clone(inputs)
	for index := range sources {
		sources[index].Data = slices.Clone(sources[index].Data)
	}
	prepared := make([]Image, len(sources))
	for index, input := range sources {
		select {
		case service.slots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		prepared[index], err = service.prepare(ctx, input)
		<-service.slots
		if err != nil {
			return nil, err
		}
	}
	refs := make([]ImageRef, len(prepared))
	for index, item := range prepared {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := service.objects.Publish(ctx, item.Ref.AttachmentID, item.Data); err != nil {
			return nil, err
		}
		refs[index] = item.Ref.Clone()
	}
	return refs, nil
}

func (service *Store) prepare(ctx context.Context, input ImageInput) (Image, error) {
	if int64(len(input.Data)) > service.settings.MaxImageBytes {
		return Image{}, &attachment.Error{
			Code:    ImageTooLarge,
			Message: "Image exceeds the configured byte limit.",
		}
	}
	if len(input.Data) == 0 {
		return Image{}, invalidRaster(nil)
	}
	if err := ctx.Err(); err != nil {
		return Image{}, err
	}
	facts, err := service.normalizer.engine.Probe(ctx, input.Data)
	if err != nil {
		return Image{}, rasterFailure(ctx, err)
	}
	isMatched := slices.Contains([]ImageMediaType{ImagePNG, ImageJPEG, ImageWebP, ImageGIF}, facts.MediaType)
	if !isMatched || facts.Width <= 0 || facts.Height <= 0 {
		return Image{}, invalidRaster(nil)
	}
	if facts.Width > service.settings.MaxImagePixels/facts.Height {
		return Image{}, &attachment.Error{
			Code:    ImageTooManyPixels,
			Message: "Image exceeds the configured decoded-pixel limit.",
		}
	}
	if max(facts.Width, facts.Height) > service.settings.MaxImageDimension {
		return Image{}, &attachment.Error{
			Code:    ImageDimensionTooLarge,
			Message: "Image exceeds the configured per-side pixel limit.",
		}
	}
	if err := service.normalizer.engine.Decode(ctx, input.Data); err != nil {
		return Image{}, rasterFailure(ctx, err)
	}
	if facts.MediaType != input.MediaType {
		return Image{}, &attachment.Error{
			Code:    ImageTypeMismatch,
			Message: "Declared image type does not match its bytes.",
		}
	}
	normalized, err := service.normalizer.Normalize(ctx, input.Data, facts)
	if err != nil {
		return Image{}, err
	}
	digest := sha256.Sum256(normalized.Data)
	ref := ImageRef{
		AttachmentID: attachment.AttachmentID("sha256:" + hex.EncodeToString(digest[:])),
		MediaType:    normalized.Ref.MediaType,
		Bytes:        int64(len(normalized.Data)),
		Width:        normalized.Ref.Width,
		Height:       normalized.Ref.Height,
		Name:         displayName(input.Name),
	}
	if normalized.Ref.Width != facts.Width || normalized.Ref.Height != facts.Height {
		ref.OriginalDimensions = &ImageDimensions{
			Width:  facts.Width,
			Height: facts.Height,
		}
	}
	return Image{
		Data: normalized.Data,
		Ref:  ref,
	}, nil
}

// ReadImage verifies storage integrity and reference metadata without decoding
// raster pixels again. Authorization belongs to the consumer's Session boundary.
func (service *Store) ReadImage(parent context.Context, ref ImageRef) (Image, error) {
	ctx, finish, err := service.begin(parent)
	if err != nil {
		return Image{}, err
	}
	defer finish()
	ref = ref.Clone()
	data, err := service.objects.Read(ctx, ref.AttachmentID, ref.Bytes)
	if err != nil {
		return Image{}, err
	}
	facts, err := service.normalizer.engine.Probe(ctx, data)
	if ctx.Err() != nil {
		return Image{}, ctx.Err()
	}
	if err != nil || facts.MediaType != ref.MediaType || facts.Width != ref.Width || facts.Height != ref.Height || int64(len(data)) != ref.Bytes {
		return Image{}, &attachment.Error{
			Code:    attachment.AttachmentCorrupt,
			Message: "Stored attachment metadata does not match its reference.",
			Cause:   err,
		}
	}
	return Image{
		Ref:  ref,
		Data: data,
	}, nil
}

// ImageHostPath locates a stored object without granting execution-world access.
func (service *Store) ImageHostPath(ref ImageRef) (string, bool, error) {
	path, err := service.objects.HostPath(ref.AttachmentID)
	return path, path != "" && err == nil, err
}

func invalidRaster(cause error) error {
	return &attachment.Error{
		Code:    InvalidImage,
		Message: "Unsupported or malformed image data.",
		Cause:   cause,
	}
}

func rasterFailure(ctx context.Context, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return invalidRaster(cause)
}

func displayName(value string) string {
	leaf := value[strings.LastIndexAny(value, `/\`)+1:]
	clean := strings.TrimSpace(strings.Map(func(character rune) rune {
		if character < 32 || character == 127 {
			return -1
		}
		return character
	}, leaf))
	units := utf16.Encode([]rune(clean))
	if len(units) > 255 {
		units = units[:255]
	}
	return string(utf16.Decode(units))
}
