package imageapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/attachment"
	"github.com/gorenx/goren/attachment/image"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
)

func (view *Reader) Attachment(ctx context.Context, call rpc.Request[AttachmentRequest]) (rpc.Outcome[AttachmentValue], error) {
	if err := ctx.Err(); err != nil {
		return rpc.Outcome[AttachmentValue]{}, err
	}
	entries, err := view.events.ReadEvents(ctx, session.SessionID(call.Payload.SessionID))
	if err != nil {
		var missing *persistence.NotFoundError
		if errors.As(err, &missing) {
			return rpc.Fail[AttachmentValue](rpc.NewRPCError(connection.ErrorSessionNotFound,
				fmt.Sprintf("session %q not found (not attached)", call.Payload.SessionID), struct {
					SessionID string `json:"sessionId"`
				}{
					SessionID: string(call.Payload.SessionID),
				})), nil
		}
		return imageReadFailure(ctx, err)
	}
	ref, err := referencedImage(ctx, entries, call.Payload.AttachmentID)
	if err != nil {
		return imageReadFailure(ctx, err)
	}
	if ref == nil {
		return imageRefusal("Image is not referenced by this session.", "ATTACHMENT_NOT_REFERENCED"), nil
	}
	if view.storage == nil {
		return imageRefusal("Image input is unavailable: this deployment mounts no attachment service.", "ATTACHMENT_UNAVAILABLE"), nil
	}
	value, err := view.storage.ReadImage(ctx, *ref)
	if err != nil {
		return imageReadFailure(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return rpc.Outcome[AttachmentValue]{}, err
	}
	return rpc.OK(AttachmentValue{
		Attachment: value.Ref.Clone(),
		Data:       base64.StdEncoding.EncodeToString(value.Data),
	}), nil
}

func imageRefusal(message, reason string) rpc.Outcome[AttachmentValue] {
	return rpc.Fail[AttachmentValue](rpc.NewRPCError(connection.ErrorAttachment, message, struct {
		Reason string `json:"reason"`
	}{
		Reason: reason,
	}))
}

func imageReadFailure(ctx context.Context, err error) (rpc.Outcome[AttachmentValue], error) {
	if ctx.Err() != nil {
		return rpc.Outcome[AttachmentValue]{}, ctx.Err()
	}
	var failure *attachment.Error
	if errors.As(err, &failure) {
		return imageRefusal(failure.Message, string(failure.Code)), nil
	}
	return rpc.Fail[AttachmentValue](rpc.NewRPCError(connection.ErrorInternal, "Unable to read image attachment.", struct{}{})), nil
}

func referencedImage(ctx context.Context, entries []session.Event, id attachment.AttachmentID) (*image.ImageRef, error) {
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry.Data, &fields); err != nil {
			return nil, err
		}
		candidates := []json.RawMessage{fields["content"]}
		var message map[string]json.RawMessage
		if json.Unmarshal(fields["message"], &message) == nil {
			candidates = append(candidates, message["content"])
		}
		var inserted []json.RawMessage
		if json.Unmarshal(fields["inserted"], &inserted) == nil {
			for _, raw := range inserted {
				var member map[string]json.RawMessage
				if json.Unmarshal(raw, &member) == nil {
					candidates = append(candidates, member["content"])
				}
			}
		}
		if entry.Type == "assistant/message" || entry.Type == "assistant/attempt" {
			var stream []struct {
				Type  string `json:"type"`
				Chunk struct {
					Type  string          `json:"type"`
					Block json.RawMessage `json:"block"`
				} `json:"chunk"`
			}
			if err := json.Unmarshal(fields["stream"], &stream); err != nil {
				return nil, err
			}
			for _, entry := range stream {
				if entry.Type == "chunk" && entry.Chunk.Type == "block-end" {
					candidates = append(candidates, append(append([]byte{'['}, entry.Chunk.Block...), ']'))
				}
			}
		}
		for _, content := range candidates {
			if found := imageInContent(content, id); found != nil {
				return found, nil
			}
		}
	}
	return nil, nil
}

func imageInContent(raw json.RawMessage, id attachment.AttachmentID) *image.ImageRef {
	var content []json.RawMessage
	if json.Unmarshal(raw, &content) != nil {
		return nil
	}
	for _, block := range content {
		var fields struct {
			Type       string          `json:"type"`
			Attachment *image.ImageRef `json:"attachment"`
			Content    json.RawMessage `json:"content"`
		}
		if json.Unmarshal(block, &fields) != nil {
			continue
		}
		if fields.Type == "image" && fields.Attachment != nil && fields.Attachment.AttachmentID == id {
			return fields.Attachment
		}
		if fields.Type == "tool-result" {
			if found := imageInContent(fields.Content, id); found != nil {
				return found
			}
		}
	}
	return nil
}

// Events provides one complete Session log snapshot without activating an Agent.
// Implementations verify the returned Session identity and report a missing
// Session as persistence.NotFoundError; they never combine different creations.
type Events interface {
	ReadEvents(ctx context.Context, id session.SessionID) ([]session.Event, error)
}

// Reader owns Session image authorization and encoded image responses.
type Reader struct {
	events  Events
	storage ImageReader
}

// NewReader binds Session events and optional image storage without owning them.
func NewReader(source Events, storage ImageReader) *Reader {
	return &Reader{
		events:  source,
		storage: storage,
	}
}
