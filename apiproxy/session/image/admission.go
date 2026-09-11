package imageapi

import (
	"context"
	"errors"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/attachment"
	"github.com/gorenx/goren/attachment/image"
	"github.com/gorenx/goren/connection"
)

// Admit decodes and saves all images as one ordered batch, or returns no references.
// Text-only requests never invoke storage.
func Admit(ctx context.Context, storage ImageStore, parts []Part) ([]image.ImageRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, nil
	}
	if storage == nil {
		return nil, &attachment.Error{
			Code:    "ATTACHMENT_UNAVAILABLE",
			Message: "Image input is unavailable: this deployment mounts no attachment service.",
		}
	}
	inputs := make([]image.ImageInput, 0, len(parts))
	for _, entry := range parts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := ""
		if entry.Name != nil {
			name = *entry.Name
		}
		input, err := image.DecodeImage(image.EncodedImage{
			MediaType: image.ImageMediaType(entry.MediaType),
			Data:      entry.Data,
			Name:      name,
		})
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, input)
	}
	refs, err := storage.SaveImages(ctx, inputs)
	if err != nil {
		return nil, err
	}
	if len(refs) != len(inputs) {
		return nil, errors.New("session API: image batch returned incorrect reference count")
	}
	return refs, ctx.Err()
}

// Rejection maps image failures to the existing Session RPC vocabulary.
func Rejection(message, reason string) connection.RPCError {
	return rpc.NewRPCError(connection.ErrorAttachment, message, struct {
		Reason string `json:"reason"`
	}{
		Reason: reason,
	})
}

// AdmissionFailure maps safe attachment errors and leaves unrelated errors to the caller.
func AdmissionFailure(err error) *connection.RPCError {
	var failure *attachment.Error
	if !errors.As(err, &failure) {
		return nil
	}
	result := Rejection(failure.Message, string(failure.Code))
	return &result
}
