// Package decoder validates original raster bytes without transforming them.
package decoder

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"

	picture "github.com/gorenx/goren/attachment/image"
	_ "golang.org/x/image/webp"
)

// Inspector reports encoded canvas dimensions and validates the first frame.
// It owns no state or native resources and never rewrites the stored file.
type Inspector struct{}

// Probe reads image headers without allocating raster pixels.
func (*Inspector) Probe(ctx context.Context, sourceBytes []byte) (picture.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return picture.Metadata{}, err
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(sourceBytes))
	if err != nil {
		return picture.Metadata{}, err
	}
	media := mediaType(format)
	if media == "" {
		return picture.Metadata{}, errors.New("attachment: unsupported raster")
	}
	return picture.Metadata{
		MediaType: media,
		Width:     int64(cfg.Width),
		Height:    int64(cfg.Height),
	}, ctx.Err()
}

// Decode validates complete decoding of the first frame after Store geometry checks.
func (*Inspector) Decode(ctx context.Context, sourceBytes []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payload := sourceBytes
	if len(sourceBytes) >= 12 && string(sourceBytes[:4]) == "RIFF" && string(sourceBytes[8:12]) == "WEBP" {
		var err error
		payload, err = webpFrame(sourceBytes)
		if err != nil {
			return err
		}
	}
	pixels, format, err := image.Decode(&input{
		ctx:    ctx,
		source: bytes.NewReader(payload),
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if pixels == nil || mediaType(format) == "" {
		return errors.New("attachment: invalid raster")
	}
	return nil
}

func mediaType(format string) picture.ImageMediaType {
	switch format {
	case "jpeg":
		return picture.ImageJPEG
	case "png":
		return picture.ImagePNG
	case "gif":
		return picture.ImageGIF
	case "webp":
		return picture.ImageWebP
	}
	return ""
}

type input struct {
	ctx    context.Context
	source io.Reader
}

func (stream *input) Read(destination []byte) (int, error) {
	if err := stream.ctx.Err(); err != nil {
		return 0, err
	}
	return stream.source.Read(destination)
}
