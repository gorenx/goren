package apiproxy

import (
	"encoding/json"
	"errors"
	"reflect"

	"github.com/gorenx/goren/connection"
)

type frameContract interface {
	frameType() string
	validate() error
}

// StreamRequest is the API Proxy's narrow server-request form. Connection
// adds the wire message type and method after the frame has been encoded.
type StreamRequest[F any] struct {
	RPCID   connection.RPCID
	Payload F
}

func encodeFrame(payload frameContract) (json.RawMessage, error) {
	if payload == nil {
		return nil, errors.New("frame is nil")
	}
	reflected := reflect.ValueOf(payload)
	if reflected.Kind() == reflect.Pointer && reflected.IsNil() {
		return nil, errors.New("frame is nil")
	}
	if err := payload.validate(); err != nil {
		return nil, err
	}
	fields, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(fields) < 2 || fields[0] != '{' || fields[len(fields)-1] != '}' {
		return nil, errors.New("frame fields must encode as an object")
	}
	kind, err := json.Marshal(payload.frameType())
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
