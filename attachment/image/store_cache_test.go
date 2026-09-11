package image

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/gorenx/goren/attachment"
	"github.com/gorenx/goren/attachment/internal/local"
)

func TestCacheSurvivesStoreReopenAndRebuildsDamage(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	backend := &scriptedEngine{
		source: Metadata{
			MediaType: ImagePNG,
			Width:     400,
			Height:    300,
			Depth:     "uchar",
			Space:     "srgb",
		},
		lengths: map[int]int{85: 8},
	}
	storage, err := local.NewObjects(root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewStore(DefaultConfig(), backend, storage)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := service.SaveImage(ctx, ImageInput{
		MediaType: ImagePNG,
		Data:      []byte("source"),
		Name:      "first.png",
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := ImageRequestPolicy{
		MaxPixels: 6400,
		MaxBytes:  10,
	}
	first, err := service.ReadImageRequest(ctx, ref, policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := local.NewObjects(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(DefaultConfig(), backend, reopened)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close(ctx) })
	ref.Name = "second.png"
	got, err := second.ReadImageRequest(ctx, ref, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(backend.attempts) != 1 || got.Image.Ref.Name != ref.Name || got.Source.Name != ref.Name || got.VariantID != first.VariantID {
		t.Fatalf("cache not reused with current occurrence: %+v", got)
	}
	// A checksummed but invalid raster must still be rejected by the image owner.
	if err := reopened.WriteCache(ctx, string(first.VariantID), []byte("not a route image")); err != nil {
		t.Fatal(err)
	}
	if _, err := second.ReadImageRequest(ctx, ref, policy); err != nil {
		t.Fatal(err)
	}
	if len(backend.attempts) != 2 {
		t.Fatal("invalid cache was not rebuilt")
	}
	// The fake engine reports source dimensions for unknown bytes, exceeding the
	// route cap above. It cannot prove real decoder behavior.
	path, err := reopened.HostPath(ref.AttachmentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = second.ReadImageRequest(ctx, ref, policy)
	var failure *attachment.Error
	if !errors.As(err, &failure) || failure.Code != attachment.AttachmentCorrupt || len(backend.attempts) != 2 {
		t.Fatalf("cache concealed corrupt source: %v", err)
	}
}

type failingCache struct {
	memoryObjects
}

func (*failingCache) WriteCache(context.Context, string, []byte) error {
	return errors.New("private disk diagnostic")
}

func TestCacheWriteFailureIsSafeAndPropagates(t *testing.T) {
	backend := &scriptedEngine{
		source: Metadata{
			MediaType: ImagePNG,
			Width:     400,
			Height:    300,
			Depth:     "uchar",
			Space:     "srgb",
		},
		lengths: map[int]int{85: 8},
	}
	service, err := NewStore(DefaultConfig(), backend, &failingCache{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close(context.Background()) })
	ref, err := service.SaveImage(context.Background(), ImageInput{
		MediaType: ImagePNG,
		Data:      []byte("source"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ReadImageRequest(context.Background(), ref, ImageRequestPolicy{
		MaxPixels: 6400,
		MaxBytes:  10,
	})
	var failure *attachment.Error
	if !errors.As(err, &failure) || failure.Code != attachment.AttachmentWriteFailed || err.Error() != "Unable to persist request image cache." {
		t.Fatalf("cache write error: %v", err)
	}
}
