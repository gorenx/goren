package imageapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/attachment"
	"github.com/gorenx/goren/attachment/image"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
)

type eventSource struct {
	entries []session.Event
	failure error
}

func (source eventSource) ReadEvents(context.Context, session.SessionID) ([]session.Event, error) {
	return source.entries, source.failure
}

type imageStorage struct {
	calls   int
	failure error
}

func (storage *imageStorage) ReadImage(ctx context.Context, ref image.ImageRef) (image.Image, error) {
	storage.calls++
	if ctx.Err() != nil {
		return image.Image{}, ctx.Err()
	}
	return image.Image{
		Ref:  ref.Clone(),
		Data: []byte{1},
	}, storage.failure
}

func imageCatalog(t *testing.T, handler *Reader) *rpc.Catalog {
	t.Helper()
	methods := rpc.NewCatalog()
	if err := rpc.RegisterUnary(methods, AttachmentMethod, DecodeAttachmentRequest, handler.Attachment); err != nil {
		t.Fatal(err)
	}
	return methods
}

func TestAttachmentAuthorizationMatchesHarnessGolden(t *testing.T) {
	data, err := os.ReadFile("testdata/image-authorization.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name    string          `json:"name"`
			Events  []session.Event `json:"events"`
			Request json.RawMessage `json:"request"`
			Value   json.RawMessage `json:"value"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, sample := range fixture.Cases {
		t.Run(sample.Name, func(t *testing.T) {
			storage := &imageStorage{}
			handler := NewReader(eventSource{
				entries: sample.Events,
			}, storage)
			result, err := imageCatalog(t, handler).DispatchUnary(context.Background(), AttachmentMethod, "read", sample.Request)
			if err != nil {
				t.Fatal(err)
			}
			if string(sample.Value) == "null" {
				if result.OK || result.Error == nil || result.Error.Code != connection.ErrorAttachment || !strings.Contains(string(result.Error.Details), "ATTACHMENT_NOT_REFERENCED") || storage.calls != 0 {
					t.Fatalf("unauthorized read: %+v", result)
				}
				return
			}
			var expected, actual any
			if err := json.Unmarshal(sample.Value, &expected); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(result.Value, &actual); err != nil {
				t.Fatal(err)
			}
			if !result.OK || !reflect.DeepEqual(expected, actual) || storage.calls != 1 {
				t.Fatalf("golden mismatch: %+v", result)
			}
		})
	}
}

func TestAttachmentReadFailuresAndCancellation(t *testing.T) {
	request := json.RawMessage(`{"sessionId":"conversation","attachmentId":"picture"}`)
	entries := []session.Event{{
		Type: "user/message",
		Data: json.RawMessage(`{"content":[{"type":"image","attachment":{"attachmentId":"picture","mediaType":"image/png","width":1,"height":1,"bytes":1}}]}`),
	}}
	for _, sample := range []struct {
		name           string
		readFailure    error
		storageFailure error
		code           connection.RPCErrorCode
		available      bool
	}{
		{
			name: "missing session",
			readFailure: &persistence.NotFoundError{
				ID: "conversation",
			},
			code: connection.ErrorSessionNotFound,
		},
		{
			name:        "unreadable log",
			readFailure: errors.New("private path"),
			code:        connection.ErrorInternal,
		},
		{
			name: "unavailable storage",
			code: connection.ErrorAttachment,
		},
		{
			name:      "corrupt object",
			available: true,
			storageFailure: &attachment.Error{
				Code:    attachment.AttachmentCorrupt,
				Message: "Stored attachment failed integrity verification.",
				Cause:   errors.New("private path"),
			},
			code: connection.ErrorAttachment,
		},
		{
			name:           "backend failure",
			available:      true,
			storageFailure: errors.New("private path"),
			code:           connection.ErrorInternal,
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			var storage ImageReader
			if sample.available {
				storage = &imageStorage{
					failure: sample.storageFailure,
				}
			}
			handler := NewReader(eventSource{
				entries: entries,
				failure: sample.readFailure,
			}, storage)
			result, err := imageCatalog(t, handler).DispatchUnary(context.Background(), AttachmentMethod, "read", request)
			if err != nil || result.OK || result.Error == nil || result.Error.Code != sample.code || strings.Contains(result.Error.Message, "private") {
				t.Fatalf("failure mapping: %+v, %v", result, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handler := NewReader(eventSource{
		entries: entries,
	}, nil)
	decoded, issues := DecodeAttachmentRequest(request)
	if len(issues) != 0 {
		t.Fatal(issues)
	}
	if _, err := handler.Attachment(ctx, rpc.Request[AttachmentRequest]{
		Payload: decoded,
	}); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
}
