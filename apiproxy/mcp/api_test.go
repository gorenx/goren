package mcpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/session"
)

type directoryStub struct {
	calls      int
	identifier session.SessionID
	name       string
	failure    error
}

func (stub *directoryStub) ListConnections(_ context.Context, identifier session.SessionID) ([]View, error) {
	stub.calls++
	stub.identifier = identifier
	return []View{}, stub.failure
}

func (stub *directoryStub) RetryConnection(_ context.Context, input RetryRequest) error {
	stub.calls++
	stub.identifier = input.SessionID
	stub.name = input.ServerName
	return stub.failure
}

func TestManagementStrictBoundary(t *testing.T) {
	for _, method := range []string{"mcp.list", "mcp.retry"} {
		for _, raw := range []string{
			`{}`, `[]`, `{"sessionId":null}`, `{"sessionId":""}`,
			`{"sessionId":"agent","serverName":"fixture","url":"http://localhost/mcp"}`,
			`{"sessionId":"agent","serverName":"fixture","command":"sh"}`,
			`{"sessionId":"agent","serverName":"fixture","creationId":"forged"}`,
		} {
			t.Run(method+raw, func(t *testing.T) {
				stub := &directoryStub{}
				methods := rpc.NewCatalog()
				if err := Register(methods, stub); err != nil {
					t.Fatal(err)
				}
				result, err := methods.DispatchUnary(t.Context(), method, "request", json.RawMessage(raw))
				if err != nil || result.OK || stub.calls != 0 {
					t.Fatalf("invalid payload reached owner: %+v, %v, calls=%d", result, err, stub.calls)
				}
			})
		}
	}
}

func TestManagementRoutesSessionAndRedactsRemoteFailure(t *testing.T) {
	for _, method := range []string{"mcp.list", "mcp.retry"} {
		t.Run(method, func(t *testing.T) {
			stub := &directoryStub{
				failure: errors.New("http://secret:credential@remote/mcp?token=hidden"),
			}
			methods := rpc.NewCatalog()
			if err := Register(methods, stub); err != nil {
				t.Fatal(err)
			}
			raw := json.RawMessage(`{"sessionId":"child"}`)
			if method == "mcp.retry" {
				raw = json.RawMessage(`{"sessionId":"child","serverName":"fixture"}`)
			}
			result, err := methods.DispatchUnary(t.Context(), method, "request", raw)
			if err != nil || result.OK || result.Error == nil || stub.identifier != "child" || stub.calls != 1 {
				t.Fatalf("wrong routing or failure: %+v, %v", result, err)
			}
			if strings.Contains(result.Error.Message, "secret") || strings.Contains(result.Error.Message, "hidden") {
				t.Fatal("remote error escaped management boundary")
			}
			for _, removed := range []string{"mcp.connect", "mcp.disconnect"} {
				outcome, _ := methods.DispatchUnary(t.Context(), removed, "removed", json.RawMessage(`{}`))
				if outcome.OK || stub.calls != 1 {
					t.Fatal("legacy management path remains registered")
				}
			}
		})
	}
}
