package apiproxy

import (
	"context"
	"errors"

	"github.com/gorenx/goren/connection"
)

// EventStreamHandler produces one logical downlink until its context is
// cancelled, the source completes, or delivery fails.
type EventStreamHandler func(context.Context, func(connection.RPCRequest) error) error

// EventStreams is the API Proxy's transport-neutral offered surface for the
// independent mux and host event streams.
type EventStreams struct {
	muxHandler  EventStreamHandler
	hostHandler EventStreamHandler
}

// NewEventStreams binds the two canonical event-stream providers.
func NewEventStreams(muxHandler EventStreamHandler, hostHandler EventStreamHandler) (*EventStreams, error) {
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
	return streams.muxHandler(requestContext, emit)
}

// Host opens the host-level event stream.
func (streams *EventStreams) Host(requestContext context.Context, emit func(connection.RPCRequest) error) error {
	return streams.hostHandler(requestContext, emit)
}
