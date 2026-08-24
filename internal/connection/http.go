package connection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorenx/goren/connection"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

const defaultMaxBodyBytes int64 = 160 << 20
const defaultGracefulTimeout = 5 * time.Second

// RPCDispatcher is the API Proxy surface consumed by the HTTP carrier.
type RPCDispatcher interface {
	HasUnary(method string) bool
	DispatchUnary(context.Context, string, connection.RPCID, json.RawMessage) (connection.RPCResult, error)
	Respond(context.Context, connection.ClientResponse) (connection.RPCReceipt, error)
}

// EventSource is the API Proxy event-stream surface consumed by the
// WebSocket carrier. Each method owns an independent stream lifetime.
type EventSource interface {
	Mux(context.Context, func(connection.RPCRequest) error) error
	Host(context.Context, func(connection.RPCRequest) error) error
}

// HTTPConfig contains the typed transport configuration owned by Connection.
type HTTPConfig struct {
	TrustedHosts    []string
	MaxBodyBytes    int64
	GracefulTimeout time.Duration
	Frontend        http.Handler
}

// HTTPHost owns the Echo transport lifecycle while exposing no Echo types to
// API Proxy or the composition root.
type HTTPHost struct {
	engine          *echo.Echo
	gracefulTimeout time.Duration
	downlinks       *webSocketDownlinks
}

// NewHTTPHost validates transport configuration and assembles the Echo carrier.
func NewHTTPHost(settings HTTPConfig, dispatch RPCDispatcher, streams EventSource) (*HTTPHost, error) {
	if dispatch == nil {
		return nil, errors.New("connection: RPC dispatcher is nil")
	}
	if streams == nil {
		return nil, errors.New("connection: event source is nil")
	}
	trusted, err := validateTrustedHosts(settings.TrustedHosts)
	if err != nil {
		return nil, err
	}
	maxBodyBytes := settings.MaxBodyBytes
	if maxBodyBytes == 0 {
		maxBodyBytes = defaultMaxBodyBytes
	}
	if maxBodyBytes < 0 {
		return nil, errors.New("connection: max body bytes must be positive")
	}
	gracefulTimeout := settings.GracefulTimeout
	if gracefulTimeout == 0 {
		gracefulTimeout = defaultGracefulTimeout
	}
	if gracefulTimeout < 0 {
		return nil, errors.New("connection: graceful timeout must be positive")
	}

	engine := echo.New()
	engine.HTTPErrorHandler = protocolHTTPErrorHandler
	engine.Pre(trustFence(trusted))
	engine.Use(middleware.RecoverWithConfig(middleware.RecoverConfig{
		DisableStackAll:   true,
		DisablePrintStack: true,
	}))
	downlinks := newWebSocketDownlinks(streams)
	engine.GET(connection.MuxEventsPath, downlinks.muxHandler())
	engine.GET(connection.HostEventsPath, downlinks.hostHandler())
	engine.POST(connection.RespondPath, respondHandler(dispatch, maxBodyBytes))
	engine.POST(connection.APIPath+"/*", unaryHandler(dispatch, maxBodyBytes))
	if settings.Frontend != nil {
		engine.RouteNotFound("/*", echo.WrapHandler(settings.Frontend))
	}
	return &HTTPHost{engine: engine, gracefulTimeout: gracefulTimeout, downlinks: downlinks}, nil
}

// Start serves requests until the lifecycle context is cancelled.
func (carrier *HTTPHost) Start(lifecycle context.Context, address string) error {
	return carrier.serve(lifecycle, echo.StartConfig{
		Address: address, GracefulTimeout: carrier.gracefulTimeout, HideBanner: true, HidePort: true,
	})
}

// Serve starts the carrier on an already-bound listener. Composition plugins
// use this form so address binding fails synchronously during activation.
func (carrier *HTTPHost) Serve(lifecycle context.Context, listener net.Listener) error {
	if listener == nil {
		return errors.New("connection: listener is nil")
	}
	return carrier.serve(lifecycle, echo.StartConfig{
		Listener: listener, GracefulTimeout: carrier.gracefulTimeout, HideBanner: true, HidePort: true,
	})
}

func (carrier *HTTPHost) serve(lifecycle context.Context, settings echo.StartConfig) error {
	serveErr := settings.Start(lifecycle, carrier.engine)
	closeContext, cancel := context.WithTimeout(context.Background(), carrier.gracefulTimeout)
	defer cancel()
	return errors.Join(serveErr, carrier.Close(closeContext))
}

// Close terminates active WebSocket downlinks and waits for their event
// sources to finish cleanup. Echo's ordinary HTTP shutdown remains owned by
// StartConfig and the lifecycle passed to Start.
func (carrier *HTTPHost) Close(closeContext context.Context) error {
	return carrier.downlinks.close(closeContext)
}

// ServeHTTP exists for embedding and transport-level tests.
func (carrier *HTTPHost) ServeHTTP(writer http.ResponseWriter, httpRequest *http.Request) {
	carrier.engine.ServeHTTP(writer, httpRequest)
}

func unaryHandler(dispatch RPCDispatcher, maxBodyBytes int64) echo.HandlerFunc {
	return func(echoContext *echo.Context) error {
		method := echoContext.Param("*")
		if echoContext.Request().URL.EscapedPath() != connection.APIPath+"/"+method {
			return writePlain(echoContext, http.StatusNotFound, "not found")
		}
		if isPrivilegedMethod(method) && !isTrustedAPIRequest(echoContext.Request(), nil) {
			return writePlain(echoContext, http.StatusForbidden, "forbidden")
		}
		body, status, err := readJSONBody(echoContext.Request(), maxBodyBytes)
		if err != nil {
			return writePlain(echoContext, status, err.Error())
		}
		if !dispatch.HasUnary(method) {
			return writePlain(echoContext, http.StatusNotFound, "not found")
		}
		message, issues := connection.DecodeClientRequest(body)
		if len(issues) != 0 {
			rpcID := salvageRPCID(body)
			return writeServerResponse(echoContext, rpcID, connection.Failure(
				connection.BadRequest("invalid client-request message", issues),
			))
		}
		if message.Method != method {
			return writeServerResponse(echoContext, message.RPCID, connection.Failure(
				connection.BadRequest(fmt.Sprintf("method %q does not match path %q", message.Method, method), nil),
			))
		}
		outcome, dispatchErr := dispatch.DispatchUnary(
			echoContext.Request().Context(), method, message.RPCID, message.Payload,
		)
		if dispatchErr != nil {
			return writePlain(echoContext, http.StatusInternalServerError, "handler failure: "+dispatchErr.Error())
		}
		return writeServerResponse(echoContext, message.RPCID, outcome)
	}
}

func respondHandler(dispatch RPCDispatcher, maxBodyBytes int64) echo.HandlerFunc {
	return func(echoContext *echo.Context) error {
		body, status, err := readJSONBody(echoContext.Request(), maxBodyBytes)
		if err != nil {
			return writePlain(echoContext, status, err.Error())
		}
		message, issues := connection.DecodeClientResponse(body)
		if len(issues) != 0 {
			return writeJSON(echoContext, http.StatusOK, connection.RejectedReceipt(connection.ReceiptBadResponse))
		}
		receipt, dispatchErr := dispatch.Respond(echoContext.Request().Context(), message)
		if dispatchErr != nil {
			return writePlain(echoContext, http.StatusInternalServerError, "handler failure: "+dispatchErr.Error())
		}
		return writeJSON(echoContext, http.StatusOK, receipt)
	}
}

func readJSONBody(httpRequest *http.Request, maxBodyBytes int64) (json.RawMessage, int, error) {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(httpRequest.Header.Get("Content-Type"), ";", 2)[0]))
	if mediaType != "application/json" {
		return nil, http.StatusUnsupportedMediaType, errors.New("content type must be application/json")
	}
	if httpRequest.ContentLength > maxBodyBytes {
		return nil, http.StatusRequestEntityTooLarge, errors.New("body exceeds configured limit")
	}
	limited := io.LimitReader(httpRequest.Body, maxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, http.StatusBadRequest, errors.New("body is not JSON")
	}
	if int64(len(body)) > maxBodyBytes {
		return nil, http.StatusRequestEntityTooLarge, errors.New("body exceeds configured limit")
	}
	if !json.Valid(body) {
		return nil, http.StatusBadRequest, errors.New("body is not JSON")
	}
	return json.RawMessage(body), http.StatusOK, nil
}

func salvageRPCID(body json.RawMessage) connection.RPCID {
	var fields struct {
		RPCID json.RawMessage `json:"rpcId"`
	}
	if json.Unmarshal(body, &fields) != nil {
		return connection.InvalidRequestRPCID
	}
	var rpcID string
	if len(fields.RPCID) == 0 || json.Unmarshal(fields.RPCID, &rpcID) != nil || strings.TrimSpace(string(fields.RPCID)) == "null" {
		return connection.InvalidRequestRPCID
	}
	return connection.RPCID(rpcID)
}

func writeServerResponse(echoContext *echo.Context, rpcID connection.RPCID, outcome connection.RPCResult) error {
	message := connection.ServerResponse{Type: connection.ServerResponseType, RPCID: rpcID, Result: outcome}
	return writeJSON(echoContext, http.StatusOK, message)
}

func writeJSON(echoContext *echo.Context, status int, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return echoContext.Blob(status, "application/json", body)
}

func writePlain(echoContext *echo.Context, status int, message string) error {
	return echoContext.String(status, message)
}

func trustFence(trusted []authority) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(echoContext *echo.Context) error {
			requestPath := echoContext.Request().URL.Path
			if (requestPath == connection.APIPath || strings.HasPrefix(requestPath, connection.APIPath+"/")) &&
				!isTrustedAPIRequest(echoContext.Request(), trusted) {
				return writePlain(echoContext, http.StatusForbidden, "forbidden")
			}
			return next(echoContext)
		}
	}
}

func protocolHTTPErrorHandler(echoContext *echo.Context, err error) {
	if response, unwrapErr := echo.UnwrapResponse(echoContext.Response()); unwrapErr == nil && response.Committed {
		return
	}
	status := http.StatusInternalServerError
	message := "internal server error"
	var statusCoder echo.HTTPStatusCoder
	if errors.As(err, &statusCoder) &&
		(statusCoder.StatusCode() == http.StatusNotFound || statusCoder.StatusCode() == http.StatusMethodNotAllowed) {
		status = http.StatusNotFound
		message = "not found"
	}
	_ = writePlain(echoContext, status, message)
}
