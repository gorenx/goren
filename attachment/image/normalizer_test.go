package image

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/gorenx/goren/attachment"
)

type scriptedEngine struct {
	mu            sync.Mutex
	source        Metadata
	lengths       map[int]int
	outputs       map[string]Metadata
	attempts      []Encoding
	brokenOutput  bool
	decodeFailure error
}

func (*scriptedEngine) EncodingVersion() string { return "scripted-test-v1" }

func (backend *scriptedEngine) Probe(_ context.Context, data []byte) (Metadata, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if facts, found := backend.outputs[string(data)]; found {
		return facts, nil
	}
	return backend.source, nil
}

func (backend *scriptedEngine) Decode(context.Context, []byte) error {
	return backend.decodeFailure
}

func (backend *scriptedEngine) Encode(_ context.Context, _ []byte, options Encoding) (Image, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.attempts = append(backend.attempts, options)
	data := bytes.Repeat([]byte{byte(options.Quality)}, backend.lengths[options.Quality])
	if backend.outputs == nil {
		backend.outputs = make(map[string]Metadata)
	}
	backend.outputs[string(data)] = Metadata{
		MediaType:       options.MediaType,
		Width:           options.Width,
		Height:          options.Height,
		CarriesMetadata: backend.brokenOutput,
		Depth:           "uchar",
		Space:           "srgb",
		HasAlpha:        backend.source.HasAlpha,
	}
	return Image{
		Data: data,
		Ref: ImageRef{
			MediaType: options.MediaType,
			Width:     options.Width,
			Height:    options.Height,
			Bytes:     int64(len(data)),
		},
	}, nil
}

func TestNormalizerQualitySelectionAndVerification(t *testing.T) {
	for _, sample := range []struct {
		name    string
		lengths map[int]int
		want    []int
		bytes   int
		alpha   bool
		broken  bool
	}{
		{
			name:    "first fit stops encoding",
			lengths: map[int]int{85: 8, 75: 4, 60: 2},
			want:    []int{85},
			bytes:   8,
		},
		{
			name:    "transparent second quality",
			lengths: map[int]int{85: 15, 75: 7, 60: 2},
			want:    []int{85, 75},
			bytes:   7,
			alpha:   true,
		},
		{
			name:    "exhaustion keeps smallest not final",
			lengths: map[int]int{85: 20, 75: 12, 60: 15},
			want:    []int{85, 75, 60},
			bytes:   12,
		},
		{
			name:    "encoder metadata violation rejected",
			lengths: map[int]int{85: 8},
			want:    []int{85},
			broken:  true,
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			backend := &scriptedEngine{
				source: Metadata{
					MediaType: ImagePNG,
					Width:     4000,
					Height:    3000,
					Depth:     "ushort",
					Space:     "srgb",
					HasAlpha:  sample.alpha,
				},
				lengths:      sample.lengths,
				brokenOutput: sample.broken,
			}
			processor, err := NewNormalizer(backend, NormalizationPolicy{
				MaxPixels:    640000,
				MaxDimension: 8192,
				MaxBytes:     10,
			})
			if err != nil {
				t.Fatal(err)
			}
			got, err := processor.Normalize(context.Background(), []byte("source"), backend.source)
			if sample.broken {
				var failure *attachment.Error
				if !errors.As(err, &failure) || failure.Code != attachment.AttachmentWriteFailed {
					t.Fatalf("output verification: %v", err)
				}
			} else if err != nil || len(got.Data) != sample.bytes || got.Ref.Width*got.Ref.Height > 640000 {
				t.Fatalf("normalized output: %+v, %v", got, err)
			}
			var qualities []int
			for _, attempt := range backend.attempts {
				qualities = append(qualities, attempt.Quality)
				wantFormat := ImageJPEG
				if sample.alpha {
					wantFormat = ImageWebP
				}
				if attempt.MediaType != wantFormat || attempt.Effort != 0 {
					t.Fatalf("encoding options: %+v", attempt)
				}
			}
			if !reflect.DeepEqual(qualities, sample.want) {
				t.Fatalf("qualities: %v, want %v", qualities, sample.want)
			}
		})
	}
}

func TestNormalizerPassthroughDetachesBytes(t *testing.T) {
	backend := &scriptedEngine{
		source: Metadata{
			MediaType: ImagePNG,
			Width:     2,
			Height:    3,
			Depth:     "uchar",
			Space:     "srgb",
		},
	}
	processor, err := NewNormalizer(backend, NormalizationPolicy{
		MaxPixels:    6,
		MaxDimension: 3,
		MaxBytes:     6,
	})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("source")
	got, err := processor.Normalize(context.Background(), data, backend.source)
	if err != nil || !bytes.Equal(got.Data, data) || len(backend.attempts) != 0 {
		t.Fatalf("passthrough: %+v, %v", got, err)
	}
	data[0] = 'X'
	if string(got.Data) != "source" {
		t.Fatal("passthrough retained caller bytes")
	}
}
