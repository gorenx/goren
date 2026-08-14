package connection

import (
	"context"

	wire "github.com/gorenx/goren/connection"
)

type testEventSource struct {
	muxStream  streamOpenFunc
	hostStream streamOpenFunc
}

func (source testEventSource) Mux(requestContext context.Context, emit func(wire.RPCRequest) error) error {
	return source.muxStream(requestContext, emit)
}

func (source testEventSource) Host(requestContext context.Context, emit func(wire.RPCRequest) error) error {
	return source.hostStream(requestContext, emit)
}

func idleEventSource() EventSource {
	idleStream := func(requestContext context.Context, _ func(wire.RPCRequest) error) error {
		<-requestContext.Done()
		return nil
	}
	return testEventSource{muxStream: idleStream, hostStream: idleStream}
}
