package connection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gorenx/goren/apiproxy"
	wire "github.com/gorenx/goren/connection"
)

func TestWebSocketDownlinksCarryIndependentStreamsAndCancelSources(t *testing.T) {
	t.Parallel()
	muxCancelled := make(chan struct{})
	hostCancelled := make(chan struct{})
	muxEvent, err := wire.NewRPCRequest("mux-1", struct {
		Type      string `json:"type"`
		SessionID string `json:"sessionId"`
		LastSeq   int    `json:"lastSeq"`
	}{Type: "session/subscribed", SessionID: "session-1", LastSeq: 4})
	if err != nil {
		t.Fatal(err)
	}
	hostEvent, err := wire.NewRPCRequest("host-1", struct {
		Type  string `json:"type"`
		Event string `json:"event"`
		Args  []any  `json:"args"`
	}{Type: "host/remote-event", Event: "commands/change", Args: []any{}})
	if err != nil {
		t.Fatal(err)
	}
	source := testEventSource{
		muxStream: func(requestContext context.Context, emit func(wire.RPCRequest) error) error {
			if err := emit(muxEvent); err != nil {
				return err
			}
			<-requestContext.Done()
			close(muxCancelled)
			return nil
		},
		hostStream: func(requestContext context.Context, emit func(wire.RPCRequest) error) error {
			if err := emit(hostEvent); err != nil {
				return err
			}
			<-requestContext.Done()
			close(hostCancelled)
			return nil
		},
	}
	carrier := webSocketTestHost(t, source)
	server := startWebSocketTestServer(t, carrier)

	muxSocket := dialWebSocket(t, server.URL, wire.MuxEventsPath, nil)
	hostSocket := dialWebSocket(t, server.URL, wire.HostEventsPath, nil)
	muxMessage := readServerRequest(t, muxSocket)
	hostMessage := readServerRequest(t, hostSocket)
	if muxMessage.RPCID != "mux-1" || muxMessage.Method != "session/subscribed" {
		t.Fatalf("mux message = %#v", muxMessage)
	}
	if hostMessage.RPCID != "host-1" || hostMessage.Method != "host/remote-event" {
		t.Fatalf("host message = %#v", hostMessage)
	}
	_ = muxSocket.Close(websocket.StatusNormalClosure, "")
	_ = hostSocket.Close(websocket.StatusNormalClosure, "")
	awaitSignal(t, muxCancelled, "mux source cancellation")
	awaitSignal(t, hostCancelled, "host source cancellation")
}

func TestWebSocketDownlinkRejectsClientMessages(t *testing.T) {
	t.Parallel()
	cancelled := make(chan struct{})
	idleStream := func(requestContext context.Context, _ func(wire.RPCRequest) error) error {
		<-requestContext.Done()
		close(cancelled)
		return nil
	}
	carrier := webSocketTestHost(t, testEventSource{muxStream: idleStream, hostStream: idleStream})
	server := startWebSocketTestServer(t, carrier)
	socket := dialWebSocket(t, server.URL, wire.MuxEventsPath, nil)
	writeContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := socket.Write(writeContext, websocket.MessageText, []byte("upstream payload")); err != nil {
		t.Fatal(err)
	}
	_, _, err := socket.Read(writeContext)
	var closeError websocket.CloseError
	if !errors.As(err, &closeError) || closeError.Code != websocket.StatusPolicyViolation || closeError.Reason != downlinkOnlyReason {
		t.Fatalf("close error = %v", err)
	}
	awaitSignal(t, cancelled, "source cancellation")
}

func TestWebSocketDownlinkSendsStreamErrorBeforeClosing(t *testing.T) {
	t.Parallel()
	sourceFailure := errors.New("mux source failed")
	idleStream := func(requestContext context.Context, _ func(wire.RPCRequest) error) error {
		<-requestContext.Done()
		return nil
	}
	carrier := webSocketTestHost(t, testEventSource{
		muxStream:  func(context.Context, func(wire.RPCRequest) error) error { return sourceFailure },
		hostStream: idleStream,
	})
	server := startWebSocketTestServer(t, carrier)
	socket := dialWebSocket(t, server.URL, wire.MuxEventsPath, nil)
	message := readServerRequest(t, socket)
	if message.Method != "stream/error" {
		t.Fatalf("method = %q", message.Method)
	}
	var frame struct {
		Type  string        `json:"type"`
		Error wire.RPCError `json:"error"`
	}
	if err := json.Unmarshal(message.Payload, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Type != "stream/error" || frame.Error.Code != wire.ErrorInternal || frame.Error.Message != sourceFailure.Error() {
		t.Fatalf("frame = %#v", frame)
	}
}

func TestWebSocketEventPathsRequireUpgrade(t *testing.T) {
	t.Parallel()
	carrier := webSocketTestHost(t, idleEventSource())
	server := startWebSocketTestServer(t, carrier)
	for _, path := range []string{wire.MuxEventsPath, wire.HostEventsPath} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != http.StatusUpgradeRequired || string(body) != "upgrade required" {
			t.Fatalf("path = %s, status = %d, body = %q", path, response.StatusCode, body)
		}
		if !strings.EqualFold(response.Header.Get("Connection"), "Upgrade") ||
			!strings.EqualFold(response.Header.Get("Upgrade"), "websocket") {
			t.Fatalf("path = %s, headers = %#v", path, response.Header)
		}
	}
}

func TestWebSocketUpgradeUsesTrustFence(t *testing.T) {
	t.Parallel()
	carrier := webSocketTestHost(t, idleEventSource())
	server := startWebSocketTestServer(t, carrier)
	header := http.Header{"Origin": []string{"http://evil.example"}}
	dialContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	socket, response, err := websocket.Dial(dialContext, webSocketURL(server.URL, wire.MuxEventsPath), &websocket.DialOptions{
		HTTPHeader: header,
	})
	if socket != nil {
		_ = socket.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("response = %#v, error = %v", response, err)
	}
	_ = response.Body.Close()
}

func TestWebSocketReconnectCreatesFreshStreamAfterOldSourceStops(t *testing.T) {
	t.Parallel()
	opened := make(chan int, 2)
	stopped := make(chan int, 2)
	invocations := 0
	muxStream := func(requestContext context.Context, emit func(wire.RPCRequest) error) error {
		invocations++
		current := invocations
		opened <- current
		event, err := wire.NewRPCRequest(wire.RPCID("mux-"+string(rune('0'+current))), struct {
			Type       string `json:"type"`
			Generation int    `json:"generation"`
		}{Type: "test/frame", Generation: current})
		if err != nil {
			return err
		}
		if err := emit(event); err != nil {
			return err
		}
		<-requestContext.Done()
		stopped <- current
		return nil
	}
	idleStream := func(requestContext context.Context, _ func(wire.RPCRequest) error) error {
		<-requestContext.Done()
		return nil
	}
	carrier := webSocketTestHost(t, testEventSource{muxStream: muxStream, hostStream: idleStream})
	server := startWebSocketTestServer(t, carrier)

	firstSocket := dialWebSocket(t, server.URL, wire.MuxEventsPath, nil)
	firstMessage := readServerRequest(t, firstSocket)
	if firstMessage.RPCID != "mux-1" {
		t.Fatalf("first rpcId = %q", firstMessage.RPCID)
	}
	_ = firstSocket.Close(websocket.StatusNormalClosure, "")
	if stoppedGeneration := awaitValue(t, stopped, "first stream cleanup"); stoppedGeneration != 1 {
		t.Fatalf("stopped generation = %d", stoppedGeneration)
	}

	secondSocket := dialWebSocket(t, server.URL, wire.MuxEventsPath, nil)
	secondMessage := readServerRequest(t, secondSocket)
	if secondMessage.RPCID != "mux-2" {
		t.Fatalf("second rpcId = %q", secondMessage.RPCID)
	}
	if firstOpened, secondOpened := awaitValue(t, opened, "first stream open"), awaitValue(t, opened, "second stream open"); firstOpened != 1 || secondOpened != 2 {
		t.Fatalf("opened = [%d %d]", firstOpened, secondOpened)
	}
	_ = secondSocket.Close(websocket.StatusNormalClosure, "")
}

func TestWebSocketTeardownWaitsForSourceCleanup(t *testing.T) {
	t.Parallel()
	streamStarted := make(chan struct{})
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	idleStream := func(requestContext context.Context, _ func(wire.RPCRequest) error) error {
		close(streamStarted)
		<-requestContext.Done()
		close(cleanupStarted)
		<-releaseCleanup
		return nil
	}
	carrier := webSocketTestHost(t, testEventSource{muxStream: idleStream, hostStream: idleStream})
	server := httptest.NewServer(carrier)
	t.Cleanup(server.Close)
	socket := dialWebSocket(t, server.URL, wire.MuxEventsPath, nil)
	awaitSignal(t, streamStarted, "stream start")
	closing := make(chan error, 1)
	go func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		closing <- carrier.Close(closeContext)
	}()
	awaitSignal(t, cleanupStarted, "source cleanup start")
	select {
	case err := <-closing:
		t.Fatalf("close returned before cleanup release: %v", err)
	default:
	}
	close(releaseCleanup)
	select {
	case err := <-closing:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not wait for source cleanup")
	}
	_ = socket.CloseNow()
}

func TestWebSocketTeardownHonorsDeadline(t *testing.T) {
	t.Parallel()
	streamStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	idleStream := func(requestContext context.Context, _ func(wire.RPCRequest) error) error {
		close(streamStarted)
		<-requestContext.Done()
		<-releaseCleanup
		return nil
	}
	carrier := webSocketTestHost(t, testEventSource{muxStream: idleStream, hostStream: idleStream})
	server := httptest.NewServer(carrier)
	t.Cleanup(server.Close)
	socket := dialWebSocket(t, server.URL, wire.MuxEventsPath, nil)
	awaitSignal(t, streamStarted, "stream start")
	closeContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := carrier.Close(closeContext)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close error = %v", err)
	}
	close(releaseCleanup)
	finishContext, finishCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer finishCancel()
	if err := carrier.Close(finishContext); err != nil {
		t.Fatal(err)
	}
	_ = socket.CloseNow()
}

func webSocketTestHost(t *testing.T, source EventSource) *HTTPHost {
	t.Helper()
	methods := apiproxy.NewCatalog()
	descriptionSource := apiproxy.HostDescriptionFunc(func(context.Context) (apiproxy.HostDescription, error) {
		return apiproxy.HostDescription{Version: "test", CWD: "/workspace"}, nil
	})
	if err := apiproxy.RegisterHostDescribe(methods, descriptionSource); err != nil {
		t.Fatal(err)
	}
	carrier, err := NewHTTPHost(HTTPConfig{}, methods, source)
	if err != nil {
		t.Fatal(err)
	}
	return carrier
}

func startWebSocketTestServer(t *testing.T, carrier *HTTPHost) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(carrier)
	t.Cleanup(func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = carrier.Close(closeContext)
		server.Close()
	})
	return server
}

func dialWebSocket(t *testing.T, serverURL string, path string, options *websocket.DialOptions) *websocket.Conn {
	t.Helper()
	dialContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	socket, response, err := websocket.Dial(dialContext, webSocketURL(serverURL, path), options)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	return socket
}

func webSocketURL(serverURL string, path string) string {
	return "ws" + strings.TrimPrefix(serverURL, "http") + path
}

func readServerRequest(t *testing.T, socket *websocket.Conn) wire.ServerRequest {
	t.Helper()
	readContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	messageType, encoded, err := socket.Read(readContext)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageText {
		t.Fatalf("message type = %v", messageType)
	}
	var message wire.ServerRequest
	if err := json.Unmarshal(encoded, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func awaitSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for " + label)
	}
}

func awaitValue(t *testing.T, values <-chan int, label string) int {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for " + label)
		return 0
	}
}
