package apiproxy

import (
	"context"
	"errors"

	"github.com/gorenx/goren/connection"
)

// MuxStreamHandler produces one typed all-session downlink until its context
// is cancelled, the source completes, or delivery fails.
type MuxStreamHandler func(context.Context, func(StreamRequest[MuxFrame]) error) error

// HostStreamHandler produces one typed host-level downlink until its context
// is cancelled, the source completes, or delivery fails.
type HostStreamHandler func(context.Context, func(StreamRequest[HostFrame]) error) error

// EventStreams is the API Proxy's transport-neutral offered surface for the
// independent mux and host event streams.
type EventStreams struct {
	muxHandler  MuxStreamHandler
	hostHandler HostStreamHandler
}

// NewEventStreams binds the two canonical event-stream providers.
func NewEventStreams(muxHandler MuxStreamHandler, hostHandler HostStreamHandler) (*EventStreams, error) {
	if muxHandler == nil {
		return nil, errors.New("apiproxy: mux event stream handler is nil")
	}
	if hostHandler == nil {
		return nil, errors.New("apiproxy: host event stream handler is nil")
	}
	return &EventStreams{muxHandler: muxHandler, hostHandler: hostHandler}, nil
}

// Mux opens the all-session mux stream.
func (streams *EventStreams) Mux(requestContext context.Context, emit func(connection.RPCRequest) error) error {
	return streams.muxHandler(requestContext, func(item StreamRequest[MuxFrame]) error {
		payload, err := EncodeMuxFrame(item.Payload)
		if err != nil {
			return err
		}
		return emit(connection.RPCRequest{RPCID: item.RPCID, Payload: payload})
	})
}

// Host opens the host-level event stream.
func (streams *EventStreams) Host(requestContext context.Context, emit func(connection.RPCRequest) error) error {
	return streams.hostHandler(requestContext, func(item StreamRequest[HostFrame]) error {
		payload, err := EncodeHostFrame(item.Payload)
		if err != nil {
			return err
		}
		return emit(connection.RPCRequest{RPCID: item.RPCID, Payload: payload})
	})
}
