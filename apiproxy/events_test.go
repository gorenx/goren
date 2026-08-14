package apiproxy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/connection"
)

func TestEventStreamsKeepMuxAndHostIndependent(t *testing.T) {
	t.Parallel()
	muxEvent := connection.RPCRequest{RPCID: "mux"}
	hostEvent := connection.RPCRequest{RPCID: "host"}
	streams, err := apiproxy.NewEventStreams(
		func(_ context.Context, emit func(connection.RPCRequest) error) error { return emit(muxEvent) },
		func(_ context.Context, emit func(connection.RPCRequest) error) error { return emit(hostEvent) },
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
	if muxReceived.RPCID != "mux" || hostReceived.RPCID != "host" {
		t.Fatalf("mux = %#v, host = %#v", muxReceived, hostReceived)
	}
}

func TestEventStreamsPreserveSourceFailure(t *testing.T) {
	t.Parallel()
	sourceFailure := errors.New("source failed")
	streams, err := apiproxy.NewEventStreams(
		func(context.Context, func(connection.RPCRequest) error) error { return sourceFailure },
		func(context.Context, func(connection.RPCRequest) error) error { return nil },
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
	idle := func(context.Context, func(connection.RPCRequest) error) error { return nil }
	if _, err := apiproxy.NewEventStreams(nil, idle); err == nil {
		t.Fatal("nil mux handler was accepted")
	}
	if _, err := apiproxy.NewEventStreams(idle, nil); err == nil {
		t.Fatal("nil host handler was accepted")
	}
}
