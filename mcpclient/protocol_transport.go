package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

type toolResultKey struct{}

var errToolResultNotCaptured = errors.New("mcpclient: MCP SDK returned without a captured Tool result")

type toolResultCapture struct {
	mutex    sync.Mutex
	value    json.RawMessage
	limit    int
	overflow bool
}

func newToolResultCapture(limit int) *toolResultCapture {
	return &toolResultCapture{
		limit: limit,
	}
}

func (capture *toolResultCapture) record(value json.RawMessage) {
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	if len(value) > capture.limit {
		capture.value = nil
		capture.overflow = true
		return
	}
	capture.value = append(json.RawMessage(nil), value...)
	capture.overflow = false
}

func (capture *toolResultCapture) result() (json.RawMessage, error) {
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	if capture.overflow {
		return nil, errors.New("mcpclient: Tool result exceeds the configured byte limit")
	}
	if capture.value == nil {
		return nil, errToolResultNotCaptured
	}
	return append(json.RawMessage(nil), capture.value...), nil
}

func contextWithToolResultCapture(
	requestContext context.Context,
	limit int,
) (context.Context, *toolResultCapture) {
	capture := newToolResultCapture(limit)
	return context.WithValue(requestContext, toolResultKey{}, capture), capture
}

type protocolTransport struct {
	target         transport.Interface
	requestTimeout time.Duration
}

func newProtocolTransport(
	target transport.Interface,
	requestTimeout time.Duration,
) (transport.Interface, error) {
	if target == nil {
		return nil, errors.New("mcpclient: transport is required")
	}
	if requestTimeout <= 0 {
		return nil, errors.New("mcpclient: request timeout must be positive")
	}
	base := &protocolTransport{
		target:         target,
		requestTimeout: requestTimeout,
	}
	bidirectional, supportsBidirectional := target.(transport.BidirectionalInterface)
	httpConnection, supportsHTTP := target.(transport.HTTPConnection)
	switch {
	case supportsBidirectional && supportsHTTP:
		return &protocolHTTPBidirectionalTransport{
			protocolTransport: base,
			bidirectional:     bidirectional,
			httpConnection:    httpConnection,
		}, nil
	case supportsBidirectional:
		return &protocolBidirectionalTransport{
			protocolTransport: base,
			bidirectional:     bidirectional,
		}, nil
	case supportsHTTP:
		return &protocolHTTPTransport{
			protocolTransport: base,
			httpConnection:    httpConnection,
		}, nil
	default:
		return base, nil
	}
}

func (connection *protocolTransport) Start(requestContext context.Context) error {
	return connection.target.Start(requestContext)
}

func (connection *protocolTransport) SendRequest(
	requestContext context.Context,
	request transport.JSONRPCRequest,
) (*transport.JSONRPCResponse, error) {
	operationContext := requestContext
	cancel := func() {}
	if request.Method != string(mcp.MethodSubscriptionsListen) {
		operationContext, cancel = context.WithTimeout(
			requestContext,
			connection.requestTimeout,
		)
	}
	defer cancel()

	response, err := connection.target.SendRequest(operationContext, request)
	if err != nil || response == nil || response.Error != nil || request.Method != string(mcp.MethodToolsCall) {
		return response, err
	}
	capture, present := requestContext.Value(toolResultKey{}).(*toolResultCapture)
	if present && capture != nil {
		capture.record(response.Result)
	}
	resultForSDK := normalizeInputRequiredToolResult(response.Result)
	if !bytes.Equal(resultForSDK, response.Result) {
		adapted := *response
		adapted.Result = resultForSDK
		return &adapted, nil
	}
	return response, nil
}

func normalizeInputRequiredToolResult(rawResult json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawResult, &fields); err != nil {
		return rawResult
	}
	var resultType mcp.ResultType
	if err := json.Unmarshal(fields["resultType"], &resultType); err != nil ||
		resultType != mcp.ResultTypeInputRequired {
		return rawResult
	}
	if _, present := fields["content"]; present {
		return rawResult
	}

	// mcp-go v1.0.0 checks for CallToolResult.content before it decodes the
	// MRTR resultType. InputRequiredResult does not have that field in the
	// 2026-07-28 protocol. Supply an empty array only to the SDK parser; the
	// capture above retains the exact wire result returned to Goren.
	fields["content"] = json.RawMessage(`[]`)
	adapted, err := json.Marshal(fields)
	if err != nil {
		return rawResult
	}
	return adapted
}

func (connection *protocolTransport) SendNotification(
	requestContext context.Context,
	notification mcp.JSONRPCNotification,
) error {
	return connection.target.SendNotification(requestContext, notification)
}

func (connection *protocolTransport) SetNotificationHandler(
	handler func(mcp.JSONRPCNotification),
) {
	connection.target.SetNotificationHandler(handler)
}

func (connection *protocolTransport) Close() error {
	return connection.target.Close()
}

func (connection *protocolTransport) GetSessionId() string {
	return connection.target.GetSessionId()
}

type protocolBidirectionalTransport struct {
	*protocolTransport
	bidirectional transport.BidirectionalInterface
}

func (connection *protocolBidirectionalTransport) SetRequestHandler(
	handler transport.RequestHandler,
) {
	connection.bidirectional.SetRequestHandler(handler)
}

type protocolHTTPTransport struct {
	*protocolTransport
	httpConnection transport.HTTPConnection
}

func (connection *protocolHTTPTransport) SetProtocolVersion(version string) {
	connection.httpConnection.SetProtocolVersion(version)
}

func (connection *protocolHTTPTransport) RequiresLegacyProtocol() bool {
	legacy, present := connection.httpConnection.(interface {
		RequiresLegacyProtocol() bool
	})
	return present && legacy.RequiresLegacyProtocol()
}

type protocolHTTPBidirectionalTransport struct {
	*protocolTransport
	bidirectional  transport.BidirectionalInterface
	httpConnection transport.HTTPConnection
}

func (connection *protocolHTTPBidirectionalTransport) SetRequestHandler(
	handler transport.RequestHandler,
) {
	connection.bidirectional.SetRequestHandler(handler)
}

func (connection *protocolHTTPBidirectionalTransport) SetProtocolVersion(version string) {
	connection.httpConnection.SetProtocolVersion(version)
}

func (connection *protocolHTTPBidirectionalTransport) RequiresLegacyProtocol() bool {
	legacy, present := connection.httpConnection.(interface {
		RequiresLegacyProtocol() bool
	})
	return present && legacy.RequiresLegacyProtocol()
}

func requireCompleteToolResult(rawResult json.RawMessage) error {
	var envelope struct {
		ResultType    mcp.ResultType   `json:"resultType"`
		InputRequests json.RawMessage  `json:"inputRequests"`
		RequestState  *json.RawMessage `json:"requestState"`
	}
	if err := json.Unmarshal(rawResult, &envelope); err != nil {
		return fmt.Errorf("mcpclient: decode raw Tool result state: %w", err)
	}
	if envelope.ResultType == mcp.ResultTypeInputRequired ||
		len(envelope.InputRequests) != 0 ||
		envelope.RequestState != nil {
		return errors.New("mcpclient: incomplete MRTR result cannot be recovered as a final Tool result")
	}
	if envelope.ResultType != "" && envelope.ResultType != mcp.ResultTypeComplete {
		return fmt.Errorf("mcpclient: unsupported Tool result type %q", envelope.ResultType)
	}
	return nil
}
