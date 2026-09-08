package stream

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/gorenx/goren/connection"
)

// Frame is a domain-owned payload accepted by the stream encoder.
type Frame interface {
	Type() string
	Validate() error
}

// Request is the API Proxy's narrow server-request form. Connection
// adds the wire message type and method after the frame has been encoded.
type Request[F Frame] struct {
	RPCID   connection.RPCID
	Payload F
}

func encodeFrame(payload Frame) (json.RawMessage, error) {
	if payload == nil {
		return nil, errors.New("frame is nil")
	}
	reflected := reflect.ValueOf(payload)
	if reflected.Kind() == reflect.Pointer && reflected.IsNil() {
		return nil, errors.New("frame is nil")
	}
	if err := payload.Validate(); err != nil {
		return nil, err
	}
	fields, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(fields) < 2 || fields[0] != '{' || fields[len(fields)-1] != '}' {
		return nil, errors.New("frame fields must encode as an object")
	}
	kind, err := json.Marshal(payload.Type())
	if err != nil {
		return nil, err
	}
	encoded := make(json.RawMessage, 0, len(fields)+len(kind)+8)
	encoded = append(encoded, `{"type":`...)
	encoded = append(encoded, kind...)
	if len(fields) > 2 {
		encoded = append(encoded, ',')
		encoded = append(encoded, fields[1:]...)
	} else {
		encoded = append(encoded, '}')
	}
	return encoded, nil
}

// MuxFrame is the payload contract for the all-session mux stream.
type MuxFrame interface {
	Frame
	MuxFrame()
}

// ErrorFrame reports a terminal stream failure. It belongs to both
// logical frame unions.
type ErrorFrame struct {
	Error connection.RPCError `json:"error"`
}

func (ErrorFrame) MuxFrame() {}

func (ErrorFrame) HostFrame() {}

func (ErrorFrame) Type() string { return "stream/error" }

func (item ErrorFrame) Validate() error {
	if !item.Error.Valid() {
		return errors.New("stream error does not match the RPC error contract")
	}
	return nil
}

// EncodeMuxFrame validates and serializes one mux payload with its canonical
// type discriminant.
func EncodeMuxFrame(payload MuxFrame) (json.RawMessage, error) {
	encoded, err := encodeFrame(payload)
	if err != nil {
		return nil, fmt.Errorf("apiproxy: encode mux frame: %w", err)
	}
	return encoded, nil
}

// HostFrame is the payload contract for host-level changes.
type HostFrame interface {
	Frame
	HostFrame()
}

// EncodeHostFrame validates and serializes one Host payload with its canonical
// type discriminant.
func EncodeHostFrame(payload HostFrame) (json.RawMessage, error) {
	encoded, err := encodeFrame(payload)
	if err != nil {
		return nil, fmt.Errorf("apiproxy: encode host frame: %w", err)
	}
	return encoded, nil
}
