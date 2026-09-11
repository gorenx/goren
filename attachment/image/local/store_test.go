package local

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorenx/goren/attachment"
	picture "github.com/gorenx/goren/attachment/image"
)

func TestOriginalImagesSurviveReopenUnchanged(t *testing.T) {
	raster := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	raster.SetNRGBA(0, 0, color.NRGBA{
		R: 200,
		A: 128,
	})
	palette := color.Palette{color.Black, color.White}
	frame := image.NewPaletted(image.Rect(0, 0, 4, 2), palette)
	for _, format := range []string{"png", "jpeg", "gif", "still.webp", "animation.webp"} {
		t.Run(format, func(t *testing.T) {
			var data bytes.Buffer
			var media picture.ImageMediaType
			var err error
			switch format {
			case "png":
				err = png.Encode(&data, raster)
				media = picture.ImagePNG
			case "jpeg":
				err = jpeg.Encode(&data, raster, nil)
				media = picture.ImageJPEG
			case "gif":
				err = gif.EncodeAll(&data, &gif.GIF{
					Image: []*image.Paletted{frame, frame},
					Delay: []int{1, 1},
				})
				media = picture.ImageGIF
			default:
				var encoded []byte
				encoded, err = os.ReadFile(filepath.Join("testdata", format))
				data.Write(encoded)
				media = picture.ImageWebP
			}
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			service, err := Open(root, picture.DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			ref, err := service.SaveImage(context.Background(), picture.ImageInput{
				MediaType: media,
				Data:      data.Bytes(),
				Name:      "../picture." + format,
			})
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(data.Bytes())
			if string(ref.AttachmentID) != "sha256:"+hex.EncodeToString(digest[:]) || ref.MediaType != media || ref.Width != 4 || ref.Height != 2 || ref.OriginalDimensions != nil {
				t.Fatalf("ref=%+v", ref)
			}
			if err := service.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(root, picture.DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close(context.Background())
			result, err := reopened.ReadImage(context.Background(), ref)
			if err != nil || !bytes.Equal(result.Data, data.Bytes()) {
				t.Fatalf("read err=%v", err)
			}
			_, err = reopened.ReadImageRequest(context.Background(), ref, picture.ImageRequestPolicy{
				MaxPixels: 100,
				MaxBytes:  1000,
			})
			var failure *attachment.Error
			if !errors.As(err, &failure) || failure.Code != "ATTACHMENT_UNAVAILABLE" {
				t.Fatalf("unexpected transform: %v", err)
			}
		})
	}
}

func TestOriginalBatchRejectsInvalidMembersBeforePublishing(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 4, 2))); err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		name   string
		media  picture.ImageMediaType
		data   []byte
		pixels int64
		code   attachment.ErrorCode
	}{
		{
			name:   "mismatch",
			media:  picture.ImageJPEG,
			data:   data.Bytes(),
			pixels: 100,
			code:   picture.ImageTypeMismatch,
		},
		{
			name:   "truncated",
			media:  picture.ImagePNG,
			data:   data.Bytes()[:len(data.Bytes())-15],
			pixels: 100,
			code:   picture.InvalidImage,
		},
		{
			name:   "pixel limit",
			media:  picture.ImagePNG,
			data:   data.Bytes(),
			pixels: 4,
			code:   picture.ImageTooManyPixels,
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			root := t.TempDir()
			settings := picture.DefaultConfig()
			settings.MaxImagePixels = sample.pixels
			service, err := Open(root, settings)
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close(context.Background())
			_, err = service.SaveImages(context.Background(), []picture.ImageInput{{
				MediaType: sample.media,
				Data:      sample.data,
			}})
			var failure *attachment.Error
			if !errors.As(err, &failure) || failure.Code != sample.code {
				t.Fatalf("error=%v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("published invalid input: %v %v", entries, err)
			}
		})
	}
}
