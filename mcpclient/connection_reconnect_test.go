package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/tools"
)

type reconnectServer struct {
	calls          atomic.Int32
	listens        atomic.Int32
	closed         atomic.Int32
	unavailable    atomic.Bool
	changed        atomic.Bool
	blockDiscovery atomic.Bool
	discovering    chan struct{}
	entered        chan struct{}
	release        chan struct{}
	disconnect     chan struct{}
}

func (remote *reconnectServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if remote.unavailable.Load() {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var incoming wireRequest
	if json.NewDecoder(request.Body).Decode(&incoming) != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	respond := func(value string) {
		fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`, incoming.ID, value)
	}
	switch incoming.Method {
	case "server/discover":
		if remote.blockDiscovery.Load() {
			select {
			case remote.discovering <- struct{}{}:
			default:
			}
			<-request.Context().Done()
			return
		}
		respond(fixtureDiscover)
	case "tools/list":
		description := "original"
		if remote.changed.Load() {
			description = "changed"
		}
		respond(fmt.Sprintf(`{"resultType":"complete","tools":[{"name":"read","description":%q,"inputSchema":{"type":"object"}}]}`, description))
	case "subscriptions/listen":
		ordinal := remote.listens.Add(1)
		defer remote.closed.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(writer, "data: %s\n\n", fixtureAck)
		writer.(http.Flusher).Flush()
		if ordinal == 1 {
			select {
			case <-remote.disconnect:
			case <-request.Context().Done():
			}
		} else {
			<-request.Context().Done()
		}
	case "tools/call":
		ordinal := remote.calls.Add(1)
		if ordinal == 1 && remote.release != nil {
			close(remote.entered)
			select {
			case <-remote.release:
			case <-request.Context().Done():
				return
			}
		}
		respond(fmt.Sprintf(`{"resultType":"complete","content":[{"type":"text","text":"call-%d"}]}`, ordinal))
	default:
		writer.WriteHeader(http.StatusBadRequest)
	}
}

func openReconnectFixture(t *testing.T, remote *reconnectServer) *Connection {
	t.Helper()
	endpoint := httptest.NewServer(remote)
	t.Cleanup(endpoint.Close)
	peer, err := Open(t.Context(), Config{
		ServerName: "fixture",
		Transport:  "streamable-http",
		URL:        endpoint.URL,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := peer.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return peer
}

func reconnectExecutor(t *testing.T, peer *Connection) tools.Executor {
	t.Helper()
	source, exists := peer.Provider(ToolName("fixture", "read"))
	if !exists {
		t.Fatal("missing provider")
	}
	implementation, err := source.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return implementation.Definition.Executor
}

func runReconnectCall(ctx context.Context, body tools.Executor) tools.BodyResult {
	return (tools.Invocation{
		Name:      ToolName("fixture", "read"),
		SessionID: "agent",
		CallID:    "read-call",
		Arguments: json.RawMessage(`{}`),
	}).Run(ctx, body)
}

func TestReconnectKeepsProviderAndAdmittedCallTransport(t *testing.T) {
	remote := &reconnectServer{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	peer := openReconnectFixture(t, remote)
	body := reconnectExecutor(t, peer)
	identifier := peer.ProviderID()
	original := peer.Done()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := make(chan tools.BodyResult, 1)
	go func() { result <- runReconnectCall(ctx, body) }()
	select {
	case <-remote.entered:
	case <-ctx.Done():
		t.Fatal("first call did not start")
	}
	retried := make(chan error, 1)
	go func() { retried <- peer.Reconnect(ctx) }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for peer.Done() == original {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("new transport was not selected while the old call remained active")
		}
	}
	if identifier != peer.ProviderID() {
		t.Fatal("retry changed configured Provider identity")
	}
	if response := runReconnectCall(ctx, body); response.Err != nil {
		t.Fatalf("installed executor did not use restored transport: %+v", response)
	}
	select {
	case response := <-result:
		t.Fatalf("reconnect interrupted the original call: %+v", response)
	case <-original:
		t.Fatal("old transport closed before its call finished")
	default:
	}
	close(remote.release)
	select {
	case response := <-result:
		if response.Err != nil {
			t.Fatalf("original call failed: %+v", response)
		}
	case <-ctx.Done():
		t.Fatal("original call did not complete")
	}
	if err := <-retried; err != nil {
		t.Fatal(err)
	}
	select {
	case <-original:
	default:
		t.Fatal("settled old transport was not released")
	}
	if remote.calls.Load() != 2 || remote.listens.Load() != 2 {
		t.Fatal("reconnect replayed work or opened extra transports")
	}
}

func TestReconnectRestoresFailedTransportAndRejectsChangedDefinition(t *testing.T) {
	remote := &reconnectServer{
		disconnect: make(chan struct{}),
	}
	peer := openReconnectFixture(t, remote)
	body := reconnectExecutor(t, peer)
	original := peer.Done()
	close(remote.disconnect)
	select {
	case <-original:
	case <-time.After(3 * time.Second):
		t.Fatal("subscription did not fail")
	}
	if err := peer.Reconnect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if response := runReconnectCall(t.Context(), body); response.Err != nil {
		t.Fatalf("old executor could not use restored connection: %+v", response)
	}
	remote.unavailable.Store(true)
	if err := peer.Reconnect(t.Context()); err == nil {
		t.Fatal("unavailable candidate succeeded")
	}
	remote.unavailable.Store(false)
	if response := runReconnectCall(t.Context(), body); response.Err != nil {
		t.Fatalf("failed candidate damaged current transport: %+v", response)
	}
	remote.changed.Store(true)
	if err := peer.Reconnect(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := remote.calls.Load()
	if response := runReconnectCall(t.Context(), body); response.Err == nil {
		t.Fatal("stale installed definition executed against changed remote definition")
	}
	if remote.calls.Load() != before {
		t.Fatal("stale definition reached the wire")
	}
	if response := runReconnectCall(t.Context(), reconnectExecutor(t, peer)); response.Err != nil {
		t.Fatalf("current definition failed: %+v", response)
	}
}

func TestCloseCancelsReconnectAndPreventsResurrection(t *testing.T) {
	remote := &reconnectServer{
		discovering: make(chan struct{}, 1),
	}
	peer := openReconnectFixture(t, remote)
	remote.blockDiscovery.Store(true)
	result := make(chan error, 1)
	go func() { result <- peer.Reconnect(t.Context()) }()
	select {
	case <-remote.discovering:
	case <-time.After(3 * time.Second):
		t.Fatal("candidate discovery did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := peer.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("reconnect succeeded after closure")
	}
	if err := peer.Reconnect(t.Context()); err == nil {
		t.Fatal("closed connection was resurrected")
	}
}
