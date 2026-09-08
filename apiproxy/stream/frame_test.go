package stream_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/apiproxy/stream"
	"github.com/gorenx/goren/connection"
)

type emptyFrame struct{}

func (emptyFrame) Type() string    { return "test/empty" }
func (emptyFrame) Validate() error { return nil }
func (emptyFrame) MuxFrame()       {}

type scalarFrame struct{ emptyFrame }

func (scalarFrame) MarshalJSON() ([]byte, error) { return []byte(`"invalid"`), nil }

type failedFrame struct{ emptyFrame }

func (failedFrame) Validate() error { return errors.New("invalid domain frame") }
func TestEncoderPreservesWireEnvelopeAndRejectsInvalidFrames(t *testing.T) {
	encoded, err := stream.EncodeMuxFrame(emptyFrame{})
	if err != nil || string(encoded) != `{"type":"test/empty"}` {
		t.Fatalf("empty frame: %s %v", encoded, err)
	}
	var typedNil *emptyFrame
	for _, payload := range []stream.MuxFrame{nil, typedNil, scalarFrame{}, failedFrame{}} {
		if _, err := stream.EncodeMuxFrame(payload); err == nil {
			t.Fatalf("accepted %T", payload)
		}
	}
	problem := stream.ErrorFrame{
		Error: connection.RPCError{
			Code:    connection.ErrorInternal,
			Message: "failure",
			Details: json.RawMessage(`{}`),
		},
	}
	mux, err := stream.EncodeMuxFrame(problem)
	if err != nil {
		t.Fatal(err)
	}
	host, err := stream.EncodeHostFrame(problem)
	if err != nil {
		t.Fatal(err)
	}
	if string(mux) != string(host) {
		t.Fatal("error envelope depends on channel")
	}
	var fields struct {
		Type  string              `json:"type"`
		Error connection.RPCError `json:"error"`
	}
	if err := json.Unmarshal(mux, &fields); err != nil {
		t.Fatal(err)
	}
	if fields.Type != "stream/error" || fields.Error.Message != "failure" || fields.Error.Code != connection.ErrorInternal {
		t.Fatalf("wire envelope: %s", mux)
	}
}
