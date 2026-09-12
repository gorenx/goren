package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	sdktransport "github.com/mark3labs/mcp-go/client/transport"
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

func (capture *toolResultCapture) readResult() (json.RawMessage, error) {
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
	target         sdktransport.Interface
	requestTimeout time.Duration
	skipDiscover   bool
}

func newProtocolTransport(
	target sdktransport.Interface,
	requestTimeout time.Duration,
	skipDiscover ...bool,
) (sdktransport.Interface, error) {
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
	if len(skipDiscover) > 0 {
		base.skipDiscover = skipDiscover[0]
	}
	bidirectional, supportsBidirectional := target.(sdktransport.BidirectionalInterface)
	httpConnection, supportsHTTP := target.(sdktransport.HTTPConnection)
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

func (wire *protocolTransport) Start(requestContext context.Context) error {
	return wire.target.Start(requestContext)
}

func (wire *protocolTransport) SendRequest(
	requestContext context.Context,
	request sdktransport.JSONRPCRequest,
) (*sdktransport.JSONRPCResponse, error) {
	if request.Method == string(mcp.MethodInitialize) {
		return nil, errors.New("mcpclient: unsupported protocol version; legacy initialize is forbidden")
	}
	if request.Method == string(mcp.MethodServerDiscover) && wire.skipDiscover {
		// SDK v1.0.0 has no stateless initialization API. This local facade only
		// primes SDK metadata; remote acceptance is checked by the first tools/list.
		return sdktransport.NewJSONRPCResultResponse(
			request.ID,
			json.RawMessage(
				`{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{}}`,
			),
		), nil
	}
	operationContext := requestContext
	cancel := func() {}
	if request.Method != string(mcp.MethodSubscriptionsListen) {
		operationContext, cancel = context.WithTimeout(
			requestContext,
			wire.requestTimeout,
		)
	}
	defer cancel()

	response, err := wire.exchange(operationContext, request)
	if err == nil &&
		response != nil &&
		response.ID.String() != request.ID.String() {
		return nil, errors.New("mcpclient: response ID does not match request")
	}
	if err != nil && operationContext.Err() != nil {
		if _, stdio := wire.target.(*sdktransport.Stdio); stdio {
			cleanup, release := context.WithTimeout(
				context.WithoutCancel(requestContext),
				min(wire.requestTimeout, time.Second),
			)
			defer release()
			notification := mcp.JSONRPCNotification{
				JSONRPC: mcp.JSONRPC_VERSION,
				Notification: mcp.Notification{
					Method: "notifications/cancelled",
					Params: mcp.NotificationParams{
						AdditionalFields: map[string]any{
							"requestId": request.ID.Value(),
						},
					},
				},
			}
			settled := make(chan struct{})
			go func() {
				defer close(settled)
				_ = wire.target.SendNotification(cleanup, notification)
			}()
			select {
			case <-settled:
			case <-cleanup.Done():
				// SDK pipe writes do not observe Context. Closing its transport
				// releases a blocked writer before we return to the ToolActor.
				_ = wire.target.Close()
				<-settled
			}
		}
	}
	if err == nil &&
		response != nil &&
		response.Error == nil {
		var envelope struct {
			ResultType        string   `json:"resultType"`
			SupportedVersions []string `json:"supportedVersions"`
		}
		if decodeErr := json.Unmarshal(response.Result, &envelope); decodeErr != nil {
			return nil, errors.New("mcpclient: invalid protocol result")
		}
		if request.Method == string(mcp.MethodServerDiscover) &&
			!slices.Contains(envelope.SupportedVersions, protocolVersion) {
			return nil, errors.New("mcpclient: unsupported protocol version in discovery")
		}
		if request.Method != string(mcp.MethodToolsCall) && envelope.ResultType != "complete" {
			return nil, errors.New("mcpclient: expected 2026-07-28 complete result")
		}
	}
	if err != nil ||
		response == nil ||
		response.Error != nil ||
		request.Method != string(mcp.MethodToolsCall) {
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

type exchangeResult struct {
	response *sdktransport.JSONRPCResponse
	err      error
}

func (wire *protocolTransport) exchange(
	ctx context.Context,
	request sdktransport.JSONRPCRequest,
) (*sdktransport.JSONRPCResponse, error) {
	if _, stdio := wire.target.(*sdktransport.Stdio); !stdio {
		return wire.target.SendRequest(ctx, request)
	}
	settled := make(chan exchangeResult, 1)
	go func() {
		response, err := wire.target.SendRequest(ctx, request)
		settled <- exchangeResult{
			response: response,
			err:      err,
		}
	}()
	select {
	case outcome := <-settled:
		return outcome.response, outcome.err
	case <-ctx.Done():
	}
	// Usually the SDK returns immediately on cancellation. A process that has
	// stopped reading can instead leave SendRequest blocked in stdin.Write.
	deadline := time.NewTimer(min(wire.requestTimeout, 250*time.Millisecond))
	defer deadline.Stop()
	select {
	case outcome := <-settled:
		return outcome.response, outcome.err
	case <-deadline.C:
		_ = wire.target.Close()
		<-settled
		return nil, ctx.Err()
	}
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

func (wire *protocolTransport) SendNotification(
	requestContext context.Context,
	notification mcp.JSONRPCNotification,
) error {
	return wire.target.SendNotification(requestContext, notification)
}

func (wire *protocolTransport) SetNotificationHandler(
	handler func(mcp.JSONRPCNotification),
) {
	wire.target.SetNotificationHandler(handler)
}

func (wire *protocolTransport) Close() error {
	return wire.target.Close()
}

func (wire *protocolTransport) GetSessionId() string {
	return wire.target.GetSessionId()
}

type protocolBidirectionalTransport struct {
	*protocolTransport
	bidirectional sdktransport.BidirectionalInterface
}

func (wire *protocolBidirectionalTransport) SetRequestHandler(
	handler sdktransport.RequestHandler,
) {
	wire.bidirectional.SetRequestHandler(handler)
}

type protocolHTTPTransport struct {
	*protocolTransport
	httpConnection sdktransport.HTTPConnection
}

func (wire *protocolHTTPTransport) SetProtocolVersion(version string) {
	wire.httpConnection.SetProtocolVersion(version)
}

func (wire *protocolHTTPTransport) RequiresLegacyProtocol() bool {
	legacy, present := wire.httpConnection.(interface {
		RequiresLegacyProtocol() bool
	})
	return present && legacy.RequiresLegacyProtocol()
}

type protocolHTTPBidirectionalTransport struct {
	*protocolTransport
	bidirectional  sdktransport.BidirectionalInterface
	httpConnection sdktransport.HTTPConnection
}

func (wire *protocolHTTPBidirectionalTransport) SetRequestHandler(
	handler sdktransport.RequestHandler,
) {
	wire.bidirectional.SetRequestHandler(handler)
}

func (wire *protocolHTTPBidirectionalTransport) SetProtocolVersion(version string) {
	wire.httpConnection.SetProtocolVersion(version)
}

func (wire *protocolHTTPBidirectionalTransport) RequiresLegacyProtocol() bool {
	legacy, present := wire.httpConnection.(interface {
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
	if envelope.ResultType != mcp.ResultTypeComplete {
		return fmt.Errorf("mcpclient: unsupported Tool result type %q", envelope.ResultType)
	}
	return nil
}
