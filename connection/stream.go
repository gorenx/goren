package connection

import (
	"encoding/json"
	"errors"
)

// RPCRequest is the narrow request form yielded by an API Proxy event stream.
// The carrier completes it into a ServerRequest by deriving method from the
// frame payload's type discriminant.
type RPCRequest struct {
	RPCID   RPCID
	Payload json.RawMessage
}

// NewRPCRequest marshals an owner-defined frame into the narrow stream form.
func NewRPCRequest(correlationID RPCID, payload any) (RPCRequest, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return RPCRequest{}, err
	}
	return RPCRequest{RPCID: correlationID, Payload: encoded}, nil
}

// EncodeServerRequest completes and encodes one narrow event-stream request.
func EncodeServerRequest(event RPCRequest) ([]byte, error) {
	if !isJSONObject(event.Payload) {
		return nil, errors.New("connection: stream frame payload must be an object")
	}
	var fields struct {
		Type json.RawMessage `json:"type"`
	}
	if err := json.Unmarshal(event.Payload, &fields); err != nil {
		return nil, errors.New("connection: stream frame payload must be an object")
	}
	method, ok := decodeString(fields.Type)
	if !ok || method == "" {
		return nil, errors.New("connection: stream frame type must be a non-empty string")
	}
	message := ServerRequest{
		Type: ServerRequestType, RPCID: event.RPCID, Method: method, Payload: cloneRaw(event.Payload),
	}
	return json.Marshal(message)
}
