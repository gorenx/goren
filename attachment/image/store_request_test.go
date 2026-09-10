package image

import (
	"context"
	"reflect"
	"testing"
)

func TestRequestImagePreservesSourceAndProjectsRoute(t *testing.T) {
	backend := &scriptedEngine{
		source: Metadata{
			MediaType: ImagePNG,
			Width:     400,
			Height:    300,
			Depth:     "uchar",
			Space:     "srgb",
		},
		lengths: map[int]int{85: 20, 75: 12, 60: 15},
	}
	storage := &memoryObjects{}
	service, err := NewStore(DefaultConfig(), backend, storage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close(context.Background()) })
	ref, err := service.SaveImage(context.Background(), ImageInput{
		MediaType: ImagePNG,
		Data:      []byte("source"),
		Name:      "photo.png",
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := ImageRequestPolicy{
		MaxPixels: 6400,
		MaxBytes:  10,
	}
	result, err := service.ReadImageRequest(context.Background(), ref, policy)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Source, ref) || ref.Width != 400 || ref.Height != 300 || len(storage.published) != 1 {
		t.Fatal("request transform modified source history or published a new durable attachment")
	}
	if len(result.Image.Data) != 12 || result.Image.Ref.Bytes != 12 || result.Image.Ref.Width*result.Image.Ref.Height > 6400 {
		t.Fatalf("request projection: %+v", result)
	}
	if result.Image.Ref.AttachmentID == ref.AttachmentID || result.Image.Ref.MediaType != ImageJPEG || result.Image.Ref.Name != ref.Name {
		t.Fatalf("request image incorrectly retained source metadata: %+v", result)
	}
	if result.Depth != "uchar" || result.Space != "srgb" || result.HasAlpha {
		t.Fatalf("request pixel facts: %+v", result)
	}
	stored, err := service.ReadImage(context.Background(), ref)
	if err != nil || string(stored.Data) != "source" {
		t.Fatalf("source read after transform: %+v, %v", stored, err)
	}
}

func TestRequestIdentityCoversTransformButNotDisplayName(t *testing.T) {
	ref := ImageRef{
		AttachmentID: "sha256:source",
		Name:         "one.png",
	}
	policy := ImageRequestPolicy{
		MaxPixels: 640000,
		MaxBytes:  1 << 20,
	}
	want, err := requestVariantID(ref, policy, "backend-v1")
	if err != nil {
		t.Fatal(err)
	}
	ref.Name = "two.png"
	got, err := requestVariantID(ref, policy, "backend-v1")
	if err != nil || got != want {
		t.Fatal("display name changed transform identity")
	}
	for _, sample := range []struct {
		pixels  int64
		bytes   int64
		version string
	}{
		{
			pixels:  640001,
			bytes:   1 << 20,
			version: "backend-v1",
		},
		{
			pixels:  640000,
			bytes:   (1 << 20) + 1,
			version: "backend-v1",
		},
		{
			pixels:  640000,
			bytes:   1 << 20,
			version: "backend-v2",
		},
	} {
		got, err := requestVariantID(ref, ImageRequestPolicy{
			MaxPixels: sample.pixels,
			MaxBytes:  sample.bytes,
		}, sample.version)
		if err != nil || got == want {
			t.Fatalf("transform identity collision: %+v, %v", sample, err)
		}
	}
}
