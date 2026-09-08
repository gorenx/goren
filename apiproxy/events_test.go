package apiproxy_test
import sessionframes "github.com/gorenx/goren/apiproxy/session/events"


import (
	"context"
	"errors"
	"github.com/gorenx/goren/apiproxy/interaction"
	"github.com/gorenx/goren/apiproxy/stream"
	"testing"

	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/connection"
)

func TestEventStreamsKeepMuxAndHostIndependent(t *testing.T) {
	t.Parallel()
	muxItem := stream.Request[stream.MuxFrame]{
		RPCID: "mux", Payload: sessionframes.SubscribedFrame{SessionID: "session-1", LastSeq: 4},
	}
	hostItem := stream.Request[stream.HostFrame]{
		RPCID: "host", Payload: sessionframes.StatusFrame{SessionID: "session-1", Running: true},
	}
	streams, err := apiproxy.NewEventStreams(
		func(_ context.Context, emit func(stream.Request[stream.MuxFrame]) error) error {
			return emit(muxItem)
		},
		func(_ context.Context, emit func(stream.Request[stream.HostFrame]) error) error {
			return emit(hostItem)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var muxReceived connection.RPCRequest
	if err := streams.Mux(context.Background(), func(event connection.RPCRequest) error {
		muxReceived = event
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var hostReceived connection.RPCRequest
	if err := streams.Host(context.Background(), func(event connection.RPCRequest) error {
		hostReceived = event
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if muxReceived.RPCID != "mux" || string(muxReceived.Payload) != `{"type":"session/subscribed","sessionId":"session-1","lastSeq":4}` {
		t.Fatalf("mux = %#v", muxReceived)
	}
	if hostReceived.RPCID != "host" || string(hostReceived.Payload) != `{"type":"host/session-status","sessionId":"session-1","running":true}` {
		t.Fatalf("mux = %#v, host = %#v", muxReceived, hostReceived)
	}
}

func TestEventStreamsPreserveSourceFailure(t *testing.T) {
	t.Parallel()
	sourceFailure := errors.New("source failed")
	streams, err := apiproxy.NewEventStreams(
		func(context.Context, func(stream.Request[stream.MuxFrame]) error) error {
			return sourceFailure
		},
		func(context.Context, func(stream.Request[stream.HostFrame]) error) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := streams.Mux(context.Background(), func(connection.RPCRequest) error { return nil }); !errors.Is(err, sourceFailure) {
		t.Fatalf("error = %v", err)
	}
}

func TestEventStreamsRequireBothProviders(t *testing.T) {
	t.Parallel()
	idleMux := func(context.Context, func(stream.Request[stream.MuxFrame]) error) error { return nil }
	idleHost := func(context.Context, func(stream.Request[stream.HostFrame]) error) error { return nil }
	if _, err := apiproxy.NewEventStreams(nil, idleHost); err == nil {
		t.Fatal("nil mux handler was accepted")
	}
	if _, err := apiproxy.NewEventStreams(idleMux, nil); err == nil {
		t.Fatal("nil host handler was accepted")
	}
}

func TestEventStreamsRejectInvalidTypedFrameBeforeCarrier(t *testing.T) {
	t.Parallel()
	streams, err := apiproxy.NewEventStreams(
		func(_ context.Context, emit func(stream.Request[stream.MuxFrame]) error) error {
			return emit(stream.Request[stream.MuxFrame]{
				RPCID: "bad", Payload: interaction.QuestionRequestedFrame{SessionID: "session-1", Questions: nil},
			})
		},
		func(context.Context, func(stream.Request[stream.HostFrame]) error) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = streams.Mux(context.Background(), func(connection.RPCRequest) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("invalid typed frame was accepted")
	}
	if called {
		t.Fatal("carrier emit was called for an invalid frame")
	}
}
