package mcpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gorenx/goren/apiproxy/rpc"
)

type directoryStub struct {
	calls   int
	failure error
}

func (stub *directoryStub) ConnectHTTP(_ context.Context, input ConnectRequest) (View, error) {
	stub.calls++
	return View{
		ServerName: input.ServerName,
		Status:     "connected",
		Transport:  "streamable-http",
		ToolCount:  2,
	}, stub.failure
}

func (stub *directoryStub) ListConnections(context.Context) ([]View, error) {
	return []View{}, nil
}

func (stub *directoryStub) Disconnect(context.Context, string) error { return nil }

func TestManagementStrictBoundary(t *testing.T) {
	for _, raw := range []string{
		`{"serverName":"fixture","url":"http://localhost/mcp","command":"sh"}`,
		`{"serverName":"fixture","url":"http://localhost/mcp","transport":"stdio"}`,
		`{"serverName":"fixture","url":"http://localhost/mcp","headers":{"Authorization":"secret"}}`,
		`{"serverName":null,"url":"http://localhost/mcp"}`,
		`{"serverName":"fixture"}`,
		`[]`,
	} {
		t.Run(raw, func(t *testing.T) {
			stub := &directoryStub{}
			methods := rpc.NewCatalog()
			if err := Register(methods, stub); err != nil {
				t.Fatal(err)
			}
			result, err := methods.DispatchUnary(t.Context(), "mcp.connect", "request", json.RawMessage(raw))
			if err != nil ||
				result.OK ||
				stub.calls != 0 {
				t.Fatalf("invalid payload reached manager: %+v, %v, calls=%d", result, err, stub.calls)
			}
		})
	}
}

func TestManagementDoesNotLeakRemoteFailure(t *testing.T) {
	stub := &directoryStub{
		failure: errors.New("http://secret:credential@remote/mcp?token=hidden"),
	}
	methods := rpc.NewCatalog()
	if err := Register(methods, stub); err != nil {
		t.Fatal(err)
	}
	result, err := methods.DispatchUnary(t.Context(), "mcp.connect", "request", json.RawMessage(`{"serverName":"fixture","url":"http://localhost/mcp"}`))
	if err != nil ||
		result.OK ||
		result.Error == nil {
		t.Fatalf("expected business failure: %+v, %v", result, err)
	}
	if strings.Contains(result.Error.Message, "secret") || strings.Contains(result.Error.Message, "hidden") {
		t.Fatal("remote error escaped management boundary")
	}
}
