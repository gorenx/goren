package connection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
	streams, err := apiproxy.NewEventStreams(
		func(requestContext context.Context, emit func(apiproxy.StreamRequest[apiproxy.MuxFrame]) error) error {
			if err := emit(apiproxy.StreamRequest[apiproxy.MuxFrame]{
				RPCID: "mux-1", Payload: apiproxy.SessionSubscribedFrame{SessionID: "session-1", LastSeq: 4},
			}); err != nil {
				return err
			}
			<-requestContext.Done()
			close(muxCancelled)
			return nil
		},
		func(requestContext context.Context, emit func(apiproxy.StreamRequest[apiproxy.HostFrame]) error) error {
			if err := emit(apiproxy.StreamRequest[apiproxy.HostFrame]{
				RPCID: "host-1", Payload: apiproxy.HostRemoteEventFrame{
					Event: "commands/change", Args: []json.RawMessage{},
				},
			}); err != nil {
				return err
			}
			<-requestContext.Done()
			close(hostCancelled)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	carrier := webSocketTestHost(t, streams)
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

func TestWebSocketSlowClientBackpressuresSourceAndShutdownUnblocksWrite(t *testing.T) {
	t.Parallel()
	largeEvent, err := wire.NewRPCRequest("large-frame", struct {
		Type string `json:"type"`
		Data string `json:"data"`
	}{Type: "test/large", Data: strings.Repeat("x", 4<<20)})
	if err != nil {
		t.Fatal(err)
	}
	var attemptedWrites atomic.Int32
	var completedWrites atomic.Int32
	sourceDone := make(chan struct{})
	muxStream := func(requestContext context.Context, emit func(wire.RPCRequest) error) error {
		defer close(sourceDone)
		for {
			attemptedWrites.Add(1)
			if err := emit(largeEvent); err != nil {
				return nil
			}
			completedWrites.Add(1)
			select {
			case <-requestContext.Done():
				return nil
			default:
			}
		}
	}
	idleStream := func(requestContext context.Context, _ func(wire.RPCRequest) error) error {
		<-requestContext.Done()
		return nil
	}
	carrier := webSocketTestHost(t, testEventSource{muxStream: muxStream, hostStream: idleStream})
	server := httptest.NewServer(carrier)
	t.Cleanup(server.Close)
	socket := dialWebSocket(t, server.URL, wire.MuxEventsPath, nil)
	t.Cleanup(func() { _ = socket.CloseNow() })

	waitForStableBackpressure(t, &attemptedWrites, &completedWrites)

	closeContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := carrier.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, sourceDone, "slow-client source cleanup")
	if completedWrites.Load() >= attemptedWrites.Load() {
		t.Fatalf("blocked write was reported delivered: attempted %d, completed %d", attemptedWrites.Load(), completedWrites.Load())
	}
}

func TestWebSocketRepeatedConnectDisconnectLeavesNoOwnedResources(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 32)
	cleaned := make(chan struct{}, 32)
	var activeSources atomic.Int32
	idleStream := func(requestContext context.Context, _ func(wire.RPCRequest) error) error {
		activeSources.Add(1)
		started <- struct{}{}
		defer func() {
			activeSources.Add(-1)
			cleaned <- struct{}{}
		}()
		<-requestContext.Done()
		return nil
	}
	carrier := webSocketTestHost(t, testEventSource{muxStream: idleStream, hostStream: idleStream})
	server := httptest.NewServer(carrier)
	t.Cleanup(server.Close)

	for connectionIndex := range 16 {
		muxSocket := dialWebSocket(t, server.URL, wire.MuxEventsPath, nil)
		hostSocket := dialWebSocket(t, server.URL, wire.HostEventsPath, nil)
		awaitSignal(t, started, "mux source start")
		awaitSignal(t, started, "host source start")
		_ = muxSocket.CloseNow()
		_ = hostSocket.CloseNow()
		awaitSignal(t, cleaned, "mux source cleanup")
		awaitSignal(t, cleaned, "host source cleanup")
		if activeSources.Load() != 0 {
			t.Fatalf("iteration %d retained %d sources", connectionIndex, activeSources.Load())
		}
	}

	closeContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := carrier.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	carrier.downlinks.mutex.Lock()
	ownedSockets := len(carrier.downlinks.sockets)
	carrier.downlinks.mutex.Unlock()
	if ownedSockets != 0 || activeSources.Load() != 0 {
		t.Fatalf("owned resources after close: sockets %d, sources %d", ownedSockets, activeSources.Load())
	}
	select {
	case <-carrier.downlinks.done:
	default:
		t.Fatal("downlink pump wait did not complete")
	}
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

func waitForStableBackpressure(t *testing.T, attemptedWrites *atomic.Int32, completedWrites *atomic.Int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		attempted := attemptedWrites.Load()
		completed := completedWrites.Load()
		if attempted > completed {
			time.Sleep(25 * time.Millisecond)
			if attemptedWrites.Load() == attempted && completedWrites.Load() == completed {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("source write did not remain backpressured: attempted %d, completed %d", attemptedWrites.Load(), completedWrites.Load())
}
