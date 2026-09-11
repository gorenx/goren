package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorenx/goren/tools"
)

func TestHTTPConnectionDrainPreservesActiveCallsAndCanResume(t *testing.T) {
	for _, mode := range []string{"finish", "cancel-wait", "close"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			canceledCall := make(chan struct{}, 2)
			remoteServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var incoming wireRequest
				if err := json.NewDecoder(request.Body).Decode(&incoming); err != nil {
					t.Error(err)
					return
				}
				respond := func(result string) {
					fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`, incoming.ID, result)
				}
				writer.Header().Set("Content-Type", "application/json")
				switch incoming.Method {
				case "server/discover":
					respond(fixtureDiscover)
				case "tools/list":
					respond(`{"resultType":"complete","tools":[{"name":"wait","inputSchema":{"type":"object"}}]}`)
				case "subscriptions/listen":
					writer.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(writer, "data: %s\n\n", fixtureAck)
					writer.(http.Flusher).Flush()
					<-request.Context().Done()
				case "tools/call":
					writer.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(writer, ": waiting\n\n")
					writer.(http.Flusher).Flush()
					entered <- struct{}{}
					select {
					case <-release:
						fmt.Fprintf(writer, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"resultType\":\"complete\",\"content\":[]}}\n\n", incoming.ID)
					case <-request.Context().Done():
						canceledCall <- struct{}{}
					}
				default:
					t.Errorf("unexpected request %s", incoming.Method)
				}
			}))
			defer remoteServer.Close()
			remote, err := Open(ctx, Config{
				ServerName: "fixture",
				Transport:  "streamable-http",
				URL:        remoteServer.URL,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer remote.Close(context.Background())
			source, exists := remote.Provider(ToolName("fixture", "wait"))
			if !exists {
				t.Fatal("missing provider")
			}
			implementation, err := source.Open(ctx)
			if err != nil {
				t.Fatal(err)
			}
			invoke := func() tools.BodyResult {
				return (tools.Invocation{
					Name:      ToolName("fixture", "wait"),
					SessionID: "drain-agent",
					CallID:    "drain-call",
					Arguments: json.RawMessage(`{}`),
				}).Run(ctx, implementation.Definition.Executor)
			}
			results := make(chan tools.BodyResult, 1)
			go func() { results <- invoke() }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("tool call did not enter the server")
			}
			waiting, stop := context.WithCancel(ctx)
			defer stop()
			drained := make(chan error, 1)
			go func() { drained <- remote.Drain(waiting) }()
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			for {
				remote.mutex.Lock()
				paused := remote.paused
				remote.mutex.Unlock()
				if paused {
					break
				}
				select {
				case <-tick.C:
				case <-ctx.Done():
					t.Fatal("Drain did not pause admission")
				}
			}
			select {
			case err := <-drained:
				t.Fatalf("Drain returned before the active call completed: %v", err)
			default:
			}
			select {
			case <-canceledCall:
				t.Fatal("Drain canceled the active HTTP call")
			default:
			}
			if _, err := source.Open(ctx); !errors.Is(err, ErrDraining) {
				t.Fatalf("new implementation admitted: %v", err)
			}
			if result := invoke(); !errors.Is(result.Err, ErrDraining) {
				t.Fatalf("new call admitted: %v", result.Err)
			}
			if err := remote.Refresh(ctx); !errors.Is(err, ErrDraining) {
				t.Fatalf("refresh admitted: %v", err)
			}
			if mode == "cancel-wait" {
				stop()
				if err := <-drained; !errors.Is(err, context.Canceled) {
					t.Fatalf("Drain cancellation: %v", err)
				}
				if result := invoke(); !errors.Is(result.Err, ErrDraining) {
					t.Fatal("canceled wait silently resumed admission")
				}
				go func() { drained <- remote.Drain(ctx) }()
			}
			if mode == "close" {
				if err := remote.Close(ctx); err != nil {
					t.Fatal(err)
				}
				if result := <-results; result.Err == nil {
					t.Fatal("Close did not cancel paused active call")
				}
				if err := remote.Resume(); err == nil {
					t.Fatal("Resume revived a closed connection")
				}
			} else {
				close(release)
				if result := <-results; result.Err != nil {
					t.Fatalf("active call failed during drain: %v", result.Err)
				}
			}
			select {
			case err := <-drained:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("Drain did not settle after the active call")
			}
			if mode != "close" {
				select {
				case <-remote.Done():
					t.Fatal("Drain closed the subscription")
				default:
				}
				if err := remote.Resume(); err != nil {
					t.Fatal(err)
				}
				if result := invoke(); result.Err != nil {
					t.Fatalf("resumed call failed: %v", result.Err)
				}
			}
		})
	}
}
