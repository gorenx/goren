package connection

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/gorenx/goren/connection"
	"github.com/labstack/echo/v5"
)

const downlinkOnlyReason = "downlink only"

type streamOpenFunc func(context.Context, func(connection.RPCRequest) error) error

type webSocketDownlinks struct {
	streams   EventSource
	lifecycle context.Context
	cancel    context.CancelFunc

	mutex   sync.Mutex
	sockets map[*websocket.Conn]struct{}
	pumps   sync.WaitGroup
	closed  bool
	done    chan struct{}
}

func newWebSocketDownlinks(streams EventSource) *webSocketDownlinks {
	lifecycle, cancel := context.WithCancel(context.Background())
	return &webSocketDownlinks{
		streams:   streams,
		lifecycle: lifecycle,
		cancel:    cancel,
		sockets:   make(map[*websocket.Conn]struct{}),
		done:      make(chan struct{}),
	}
}

func (downlinks *webSocketDownlinks) muxHandler() echo.HandlerFunc {
	return downlinks.handler(downlinks.streams.Mux)
}

func (downlinks *webSocketDownlinks) hostHandler() echo.HandlerFunc {
	return downlinks.handler(downlinks.streams.Host)
}

func (downlinks *webSocketDownlinks) handler(openStream streamOpenFunc) echo.HandlerFunc {
	return func(echoContext *echo.Context) error {
		if !isWebSocketUpgrade(echoContext.Request()) {
			echoContext.Response().Header().Set("Connection", "Upgrade")
			echoContext.Response().Header().Set("Upgrade", "websocket")
			return writePlain(echoContext, http.StatusUpgradeRequired, "upgrade required")
		}
		socket, err := websocket.Accept(echoContext.Response(), echoContext.Request(), nil)
		if err != nil {
			return nil
		}
		if !downlinks.register(socket) {
			_ = socket.CloseNow()
			return nil
		}
		defer downlinks.unregister(socket)
		downlinks.pump(socket, openStream)
		return nil
	}
}

func (downlinks *webSocketDownlinks) register(socket *websocket.Conn) bool {
	downlinks.mutex.Lock()
	defer downlinks.mutex.Unlock()
	if downlinks.closed {
		return false
	}
	downlinks.sockets[socket] = struct{}{}
	downlinks.pumps.Add(1)
	return true
}

func (downlinks *webSocketDownlinks) unregister(socket *websocket.Conn) {
	_ = socket.CloseNow()
	downlinks.mutex.Lock()
	delete(downlinks.sockets, socket)
	downlinks.mutex.Unlock()
	downlinks.pumps.Done()
}

func (downlinks *webSocketDownlinks) pump(socket *websocket.Conn, openStream streamOpenFunc) {
	streamContext, cancelStream := context.WithCancel(downlinks.lifecycle)
	defer cancelStream()
	readDone := make(chan bool, 1)
	go monitorClientMessages(streamContext, cancelStream, socket, readDone)

	streamErr := openStream(streamContext, func(event connection.RPCRequest) error {
		encoded, err := connection.EncodeServerRequest(event)
		if err != nil {
			return err
		}
		return socket.Write(streamContext, websocket.MessageText, encoded)
	})
	if streamContext.Err() == nil {
		if streamErr != nil {
			_ = sendStreamFailure(streamContext, socket, streamErr)
		}
		_ = socket.Close(websocket.StatusNormalClosure, "")
	}
	cancelStream()
	if clientSentMessage := <-readDone; clientSentMessage {
		_ = socket.Close(websocket.StatusPolicyViolation, downlinkOnlyReason)
	}
	_ = socket.CloseNow()
}

func monitorClientMessages(
	streamContext context.Context,
	cancelStream context.CancelFunc,
	socket *websocket.Conn,
	readDone chan<- bool,
) {
	_, _, err := socket.Read(streamContext)
	cancelStream()
	readDone <- err == nil
}

func sendStreamFailure(streamContext context.Context, socket *websocket.Conn, streamErr error) error {
	rpcID, err := mintRPCID()
	if err != nil {
		return err
	}
	event, err := connection.NewRPCRequest(rpcID, struct {
		Type  string              `json:"type"`
		Error connection.RPCError `json:"error"`
	}{
		Type: "stream/error",
		Error: connection.RPCError{
			Code: connection.ErrorInternal, Message: streamErr.Error(), Details: json.RawMessage(`{}`),
		},
	})
	if err != nil {
		return err
	}
	encoded, err := connection.EncodeServerRequest(event)
	if err != nil {
		return err
	}
	return socket.Write(streamContext, websocket.MessageText, encoded)
}

func mintRPCID() (connection.RPCID, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", fmt.Errorf("connection: mint stream rpc id: %w", err)
	}
	randomBytes[6] = (randomBytes[6] & 0x0f) | 0x40
	randomBytes[8] = (randomBytes[8] & 0x3f) | 0x80
	return connection.RPCID(fmt.Sprintf(
		"%x-%x-%x-%x-%x",
		randomBytes[0:4], randomBytes[4:6], randomBytes[6:8], randomBytes[8:10], randomBytes[10:16],
	)), nil
}

func (downlinks *webSocketDownlinks) close(closeContext context.Context) error {
	downlinks.mutex.Lock()
	if !downlinks.closed {
		downlinks.closed = true
		downlinks.cancel()
		activeSockets := make([]*websocket.Conn, 0, len(downlinks.sockets))
		for socket := range downlinks.sockets {
			activeSockets = append(activeSockets, socket)
		}
		go func() {
			for _, socket := range activeSockets {
				_ = socket.CloseNow()
			}
			downlinks.pumps.Wait()
			close(downlinks.done)
		}()
	}
	done := downlinks.done
	downlinks.mutex.Unlock()
	select {
	case <-done:
		return nil
	case <-closeContext.Done():
		return fmt.Errorf("connection: close WebSocket downlinks: %w", closeContext.Err())
	}
}

func isWebSocketUpgrade(httpRequest *http.Request) bool {
	return headerHasToken(httpRequest.Header.Values("Connection"), "upgrade") &&
		headerHasToken(httpRequest.Header.Values("Upgrade"), "websocket")
}

func headerHasToken(values []string, token string) bool {
	for _, value := range values {
		for part := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}
