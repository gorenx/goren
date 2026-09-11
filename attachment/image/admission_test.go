package image_test

import (
	"errors"
	"testing"

	"github.com/gorenx/goren/attachment"
	"github.com/gorenx/goren/attachment/image"
)

func TestDecodeImageRequiresCanonicalBase64(t *testing.T) {
	t.Parallel()
	for _, encoded := range []string{"", "AA", "AB==", "AA==\n", " AA==", "AA==!", "AA--"} {
		_, err := image.DecodeImage(image.EncodedImage{
			MediaType: image.ImagePNG,
			Data:      encoded,
		})
		var failure *attachment.Error
		if !errors.As(err, &failure) || failure.Code != image.InvalidImageBase64 || !image.IsAdmission(err) {
			t.Fatalf("%q: %v", encoded, err)
		}
	}
	got, err := image.DecodeImage(image.EncodedImage{
		MediaType: image.ImagePNG,
		Data:      "AA==",
		Name:      "sample.png",
	})
	if err != nil || len(got.Data) != 1 || got.Data[0] != 0 || got.Name != "sample.png" || got.MediaType != image.ImagePNG {
		t.Fatalf("canonical upload: %+v, %v", got, err)
	}
}

func TestBatchAdmissionPreservesErrorOrder(t *testing.T) {
	t.Parallel()
	settings := image.DefaultConfig()
	limits := settings.Limits()
	limits.MaxImagesPerMessage = 1
	limits.MaxMessageImageBytes = 1
	bad := image.ImageInput{
		MediaType: "image/svg+xml",
		Data:      []byte{0, 1},
	}
	for _, sample := range []struct {
		inputs []image.ImageInput
		want   attachment.ErrorCode
	}{
		{
			inputs: []image.ImageInput{bad, bad},
			want:   image.TooManyImages,
		},
		{
			inputs: []image.ImageInput{bad},
			want:   image.ImagesTooLarge,
		},
		{
			inputs: []image.ImageInput{{
				MediaType: "image/svg+xml",
				Data:      []byte{0},
			}},
			want: image.UnsupportedImageType,
		},
	} {
		err := limits.ValidateBatch(sample.inputs)
		var failure *attachment.Error
		if !errors.As(err, &failure) || failure.Code != sample.want {
			t.Fatalf("batch failure = %v; want %s", err, sample.want)
		}
	}
	if err := limits.ValidateBatch([]image.ImageInput{{
		MediaType: image.ImagePNG,
		Data:      []byte{0},
	}}); err != nil {
		t.Fatalf("batch at exact limit: %v", err)
	}
}

func TestConfigRejectsExplicitInvalidValuesAndDetachesLimits(t *testing.T) {
	t.Parallel()
	settings := image.DefaultConfig()
	if err := settings.Validate(); err != nil {
		t.Fatal(err)
	}
	first := settings.Limits()
	first.MediaTypes[0] = "image/svg+xml"
	if settings.Limits().MediaTypes[0] != image.ImagePNG {
		t.Fatal("limits snapshots share mutable format list")
	}
	for _, change := range []func(*image.Config){
		func(target *image.Config) { target.MaxImageBytes = 0 },
		func(target *image.Config) { target.MaxImagesPerMessage = -1 },
		func(target *image.Config) { target.MaxMessageImageBytes = 1 << 53 },
		func(target *image.Config) { target.MaxImagePixels = 0 },
		func(target *image.Config) { target.MaxImageDimension = 0 },
		func(target *image.Config) { target.NormalizedImageMaxPixels = 0 },
		func(target *image.Config) { target.NormalizedImageMaxDimension = 0 },
		func(target *image.Config) { target.NormalizedImageMaxBytes = 0 },
		func(target *image.Config) { target.ImageCompressionConcurrency = 0 },
		func(target *image.Config) { target.ImageCompressionConcurrency = 9 },
	} {
		invalid := settings
		change(&invalid)
		if err := invalid.Validate(); err == nil {
			t.Fatalf("accepted invalid configuration: %+v", invalid)
		}
	}
}
