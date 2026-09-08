package apiproxy

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/apiproxy/stream"
	"github.com/gorenx/goren/connection"
)

// HasUnary reports registered methods while the API accepts requests.
func (owner *Server) HasUnary(method string) bool {
	return !owner.closed.Load() && owner.methods.HasUnary(method)
}

// DispatchUnary delegates strict decoding and invocation to the API catalog.
func (owner *Server) DispatchUnary(ctx context.Context, method string, id connection.RPCID, payload json.RawMessage) (connection.RPCResult, error) {
	if owner.closed.Load() {
		return connection.RPCResult{}, stream.ErrClosed
	}
	return owner.methods.DispatchUnary(ctx, method, id, payload)
}

// Respond correlates a client answer with its outstanding interaction.
func (owner *Server) Respond(ctx context.Context, response connection.ClientResponse) (connection.RPCReceipt, error) {
	if owner.closed.Load() {
		return connection.RPCReceipt{}, stream.ErrClosed
	}
	return owner.methods.Respond(ctx, response)
}

// Mux opens a Session baseline and subsequent all-session notifications.
func (owner *Server) Mux(ctx context.Context, emit func(connection.RPCRequest) error) error {
	if owner.closed.Load() {
		return stream.ErrClosed
	}
	return owner.sessions.Mux(ctx, emit)
}

// Host opens an independent downlink of host-level notifications.
func (owner *Server) Host(ctx context.Context, emit func(connection.RPCRequest) error) error {
	if owner.closed.Load() {
		return stream.ErrClosed
	}
	subscription, err := owner.broker.OpenHost()
	if err != nil {
		return err
	}
	defer owner.broker.Release(subscription)
	for {
		frames, found, err := subscription.Next(ctx)
		if err != nil || !found {
			return err
		}
		for _, frame := range frames {
			if err := emit(frame); err != nil {
				return err
			}
		}
	}
}
