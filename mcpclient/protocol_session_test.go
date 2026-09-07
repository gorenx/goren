package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	sdkclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

type requestObservation struct {
	method      string
	id          any
	params      json.RawMessage
	header      http.Header
	hasDeadline bool
}

type fixtureTransport struct {
	mutex               sync.Mutex
	responder           func(context.Context, transport.JSONRPCRequest) (*transport.JSONRPCResponse, error)
	observations        []requestObservation
	notificationHandler func(mcp.JSONRPCNotification)
	requestHandler      transport.RequestHandler
	protocolVersion     string
	started             bool
	closed              bool
}

func (fixture *fixtureTransport) Start(context.Context) error {
	fixture.mutex.Lock()
	defer fixture.mutex.Unlock()
	fixture.started = true
	return nil
}

func (fixture *fixtureTransport) SendRequest(
	requestContext context.Context,
	request transport.JSONRPCRequest,
) (*transport.JSONRPCResponse, error) {
	params, err := json.Marshal(request.Params)
	if err != nil {
		return nil, err
	}
	_, hasDeadline := requestContext.Deadline()
	fixture.mutex.Lock()
	fixture.observations = append(
		fixture.observations,
		requestObservation{
			method:      request.Method,
			id:          request.ID.Value(),
			params:      append(json.RawMessage(nil), params...),
			header:      request.Header.Clone(),
			hasDeadline: hasDeadline,
		},
	)
	responder := fixture.responder
	fixture.mutex.Unlock()
	if responder == nil {
		return nil, errors.New("fixture transport: responder is required")
	}
	return responder(requestContext, request)
}

func (fixture *fixtureTransport) SendNotification(
	context.Context,
	mcp.JSONRPCNotification,
) error {
	return nil
}

func (fixture *fixtureTransport) SetNotificationHandler(
	handler func(mcp.JSONRPCNotification),
) {
	fixture.mutex.Lock()
	defer fixture.mutex.Unlock()
	fixture.notificationHandler = handler
}

func (fixture *fixtureTransport) SetRequestHandler(handler transport.RequestHandler) {
	fixture.mutex.Lock()
	defer fixture.mutex.Unlock()
	fixture.requestHandler = handler
}

func (fixture *fixtureTransport) SetProtocolVersion(version string) {
	fixture.mutex.Lock()
	defer fixture.mutex.Unlock()
	fixture.protocolVersion = version
}

func (fixture *fixtureTransport) Close() error {
	fixture.mutex.Lock()
	defer fixture.mutex.Unlock()
	fixture.closed = true
	return nil
}

func (fixture *fixtureTransport) GetSessionId() string {
	return "fixture"
}

func (fixture *fixtureTransport) snapshot() (
	[]requestObservation,
	string,
	bool,
	bool,
) {
	fixture.mutex.Lock()
	defer fixture.mutex.Unlock()
	observations := append([]requestObservation(nil), fixture.observations...)
	return observations, fixture.protocolVersion, fixture.started, fixture.closed
}

type fixtureElicitor struct {
	elicit func(context.Context, mcp.ElicitationRequest) (*mcp.ElicitationResult, error)
}

func (elicitor *fixtureElicitor) Elicit(
	requestContext context.Context,
	request mcp.ElicitationRequest,
) (*mcp.ElicitationResult, error) {
	return elicitor.elicit(requestContext, request)
}

func protocolTestSettings(elicitation sdkclient.ElicitationHandler) protocolSettings {
	return protocolSettings{
		identity: mcp.Implementation{
			Name:    "goren-test",
			Version: "1.0.0",
		},
		elicitation:        elicitation,
		requestTimeout:     250 * time.Millisecond,
		toolResultLimit:    4096,
		maxInputRoundTrips: 3,
	}
}

func discoverResponse(requestID mcp.RequestId) *transport.JSONRPCResponse {
	return transport.NewJSONRPCResultResponse(
		requestID,
		json.RawMessage(`{
			"resultType":"complete",
			"supportedVersions":["2026-07-28"],
			"capabilities":{"tools":{"listChanged":true}},
			"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"fixture","version":"1.0.0"}}
		}`),
	)
}

func startProtocolTestSession(
	testingContext *testing.T,
	fixture *fixtureTransport,
	elicitation sdkclient.ElicitationHandler,
) *protocolSession {
	testingContext.Helper()
	session, err := newProtocolSession(
		fixture,
		protocolTestSettings(elicitation),
	)
	if err != nil {
		testingContext.Fatalf("newProtocolSession() error = %v", err)
	}
	if err := session.start(testingContext.Context()); err != nil {
		testingContext.Fatalf("start() error = %v", err)
	}
	testingContext.Cleanup(func() {
		_ = session.close()
	})
	return session
}

func TestProtocolSessionStartDiscoversExactVersionAndCapabilities(t *testing.T) {
	fixture := &fixtureTransport{}
	fixture.responder = func(
		_ context.Context,
		request transport.JSONRPCRequest,
	) (*transport.JSONRPCResponse, error) {
		if request.Method != string(mcp.MethodServerDiscover) {
			t.Fatalf("request method = %q, want %q", request.Method, mcp.MethodServerDiscover)
		}
		return discoverResponse(request.ID), nil
	}
	elicitor := &fixtureElicitor{
		elicit: func(
			context.Context,
			mcp.ElicitationRequest,
		) (*mcp.ElicitationResult, error) {
			t.Fatal("elicitation handler was called during discovery")
			return nil, nil
		},
	}
	session := startProtocolTestSession(t, fixture, elicitor)
	if session.client.ProtocolVersion() != protocolVersion {
		t.Fatalf("protocol version = %q, want %q", session.client.ProtocolVersion(), protocolVersion)
	}

	observations, negotiated, started, _ := fixture.snapshot()
	if !started {
		t.Fatal("transport was not started")
	}
	if negotiated != protocolVersion {
		t.Fatalf("transport protocol version = %q, want %q", negotiated, protocolVersion)
	}
	if len(observations) != 1 {
		t.Fatalf("request count = %d, want 1", len(observations))
	}
	request := observations[0]
	if request.header.Get(mcp.HeaderMethod) != string(mcp.MethodServerDiscover) {
		t.Fatalf("%s = %q", mcp.HeaderMethod, request.header.Get(mcp.HeaderMethod))
	}
	var params mcp.DiscoverParams
	if err := json.Unmarshal(request.params, &params); err != nil {
		t.Fatalf("decode discover params: %v", err)
	}
	if params.Meta.ProtocolVersion() != protocolVersion {
		t.Fatalf("request protocol version = %q", params.Meta.ProtocolVersion())
	}
	identity := params.Meta.ClientInfo()
	if identity == nil || identity.Name != "goren-test" || identity.Version != "1.0.0" {
		t.Fatalf("client identity = %#v", identity)
	}
	capabilities := params.Meta.ClientCapabilities()
	if capabilities == nil || capabilities.Elicitation == nil {
		t.Fatal("elicitation capability was not advertised")
	}
	if capabilities.Elicitation.Form == nil || capabilities.Elicitation.URL == nil {
		t.Fatalf("elicitation modes = %#v, want form and url", capabilities.Elicitation)
	}
	if capabilities.Sampling != nil || capabilities.Roots != nil || capabilities.Tasks != nil {
		t.Fatalf("unexpected client capabilities = %#v", capabilities)
	}
}

func TestProtocolSessionStartRejectsLegacyFallback(t *testing.T) {
	fixture := &fixtureTransport{}
	fixture.responder = func(
		_ context.Context,
		request transport.JSONRPCRequest,
	) (*transport.JSONRPCResponse, error) {
		switch request.Method {
		case string(mcp.MethodServerDiscover):
			return transport.NewJSONRPCErrorResponse(
				request.ID,
				mcp.METHOD_NOT_FOUND,
				"method not found",
				nil,
			), nil
		case string(mcp.MethodInitialize):
			return transport.NewJSONRPCResultResponse(
				request.ID,
				json.RawMessage(`{
					"protocolVersion":"2025-11-25",
					"capabilities":{"tools":{}},
					"serverInfo":{"name":"legacy","version":"1.0.0"}
				}`),
			), nil
		default:
			t.Fatalf("unexpected request method %q", request.Method)
			return nil, nil
		}
	}
	session, err := newProtocolSession(
		fixture,
		protocolTestSettings(nil),
	)
	if err != nil {
		t.Fatalf("newProtocolSession() error = %v", err)
	}
	err = session.start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "unsupported protocol version") {
		t.Fatalf("start() error = %v, want unsupported protocol version", err)
	}
	observations, _, _, closed := fixture.snapshot()
	if !closed {
		t.Fatal("legacy connection was not closed")
	}
	if len(observations) != 2 {
		t.Fatalf("request count = %d, want discover plus legacy initialize", len(observations))
	}
}

type protocolContextKey struct{}

func TestProtocolSessionCallToolCompletesElicitationRoundTrip(t *testing.T) {
	const contextValue = "root-agent-call"
	callCount := 0
	fixture := &fixtureTransport{}
	fixture.responder = func(
		_ context.Context,
		request transport.JSONRPCRequest,
	) (*transport.JSONRPCResponse, error) {
		if request.Method == string(mcp.MethodServerDiscover) {
			return discoverResponse(request.ID), nil
		}
		if request.Method != string(mcp.MethodToolsCall) {
			t.Fatalf("unexpected request method %q", request.Method)
		}
		callCount++
		if callCount == 1 {
			result := mcp.NewInputRequiredResult(
				mcp.InputRequests{
					"approval": mcp.NewElicitationInputRequest(
						mcp.ElicitationParams{
							Mode:    mcp.ElicitationModeForm,
							Message: "Approve the operation",
							RequestedSchema: map[string]any{
								"type": "object",
								"properties": map[string]any{
									"approved": map[string]any{
										"type": "boolean",
									},
								},
							},
						},
					),
				},
				"opaque-state",
			)
			rawResult, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("marshal input-required result: %v", err)
			}
			return transport.NewJSONRPCResultResponse(request.ID, rawResult), nil
		}
		return transport.NewJSONRPCResultResponse(
			request.ID,
			json.RawMessage(`{
				"resultType":"complete",
				"content":[{"type":"text","text":"done"}],
				"structuredContent":{"approved":true}
			}`),
		), nil
	}
	elicitor := &fixtureElicitor{
		elicit: func(
			requestContext context.Context,
			request mcp.ElicitationRequest,
		) (*mcp.ElicitationResult, error) {
			if requestContext.Value(protocolContextKey{}) != contextValue {
				t.Fatal("elicitation did not retain the Tool call Context")
			}
			if request.Params.Mode != mcp.ElicitationModeForm {
				t.Fatalf("elicitation mode = %q", request.Params.Mode)
			}
			return &mcp.ElicitationResult{
				ElicitationResponse: mcp.ElicitationResponse{
					Action: mcp.ElicitationResponseActionAccept,
					Content: map[string]any{
						"approved": true,
					},
				},
			}, nil
		},
	}
	session := startProtocolTestSession(t, fixture, elicitor)
	requestContext := context.WithValue(
		t.Context(),
		protocolContextKey{},
		contextValue,
	)
	rawResult, err := session.callTool(
		requestContext,
		"approve_operation",
		json.RawMessage(`{"path":"notes.md"}`),
	)
	if err != nil {
		t.Fatalf("callTool() error = %v", err)
	}
	if string(rawResult) != `{
				"resultType":"complete",
				"content":[{"type":"text","text":"done"}],
				"structuredContent":{"approved":true}
			}` {
		t.Fatalf("raw result was not preserved: %s", rawResult)
	}

	observations, _, _, _ := fixture.snapshot()
	if len(observations) != 3 {
		t.Fatalf("request count = %d, want discover plus two Tool rounds", len(observations))
	}
	first := observations[1]
	second := observations[2]
	if first.id == second.id {
		t.Fatalf("MRTR reused request ID %v", first.id)
	}
	if first.header.Get(mcp.HeaderMethod) != string(mcp.MethodToolsCall) ||
		first.header.Get(mcp.HeaderName) != "approve_operation" {
		t.Fatalf("Tool headers = %#v", first.header)
	}
	var params struct {
		Name           string                     `json:"name"`
		Arguments      json.RawMessage            `json:"arguments"`
		InputResponses map[string]json.RawMessage `json:"inputResponses"`
		RequestState   string                     `json:"requestState"`
		Meta           *mcp.Meta                  `json:"_meta"`
	}
	if err := json.Unmarshal(second.params, &params); err != nil {
		t.Fatalf("decode second Tool request: %v", err)
	}
	if params.Name != "approve_operation" || string(params.Arguments) != `{"path":"notes.md"}` {
		t.Fatalf("retried Tool request changed original params: %s", second.params)
	}
	if params.RequestState != "opaque-state" {
		t.Fatalf("requestState = %q", params.RequestState)
	}
	response, present := params.InputResponses["approval"]
	if !present || !strings.Contains(string(response), `"action":"accept"`) ||
		!strings.Contains(string(response), `"approved":true`) {
		t.Fatalf("input response = %s", response)
	}
	if params.Meta.ProtocolVersion() != protocolVersion {
		t.Fatalf("retry protocol version = %q", params.Meta.ProtocolVersion())
	}
}

func TestProtocolSessionCallToolPreservesUnknownCompleteContent(t *testing.T) {
	unknownResult := json.RawMessage(`{
		"resultType":"complete",
		"content":[{"type":"future-content","value":1}],
		"structuredContent":{"answer":42}
	}`)
	fixture := &fixtureTransport{}
	fixture.responder = func(
		_ context.Context,
		request transport.JSONRPCRequest,
	) (*transport.JSONRPCResponse, error) {
		if request.Method == string(mcp.MethodServerDiscover) {
			return discoverResponse(request.ID), nil
		}
		return transport.NewJSONRPCResultResponse(request.ID, unknownResult), nil
	}
	session := startProtocolTestSession(t, fixture, nil)
	rawResult, err := session.callTool(
		t.Context(),
		"future_tool",
		json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatalf("callTool() error = %v", err)
	}
	if string(rawResult) != string(unknownResult) {
		t.Fatalf("raw result = %s, want %s", rawResult, unknownResult)
	}
}

func TestProtocolSessionCallToolDoesNotRecoverInputRequiredOnHandlerError(t *testing.T) {
	fixture := &fixtureTransport{}
	fixture.responder = func(
		_ context.Context,
		request transport.JSONRPCRequest,
	) (*transport.JSONRPCResponse, error) {
		if request.Method == string(mcp.MethodServerDiscover) {
			return discoverResponse(request.ID), nil
		}
		return transport.NewJSONRPCResultResponse(
			request.ID,
			json.RawMessage(`{
				"resultType":"input_required",
				"inputRequests":{
					"question":{
						"method":"elicitation/create",
						"params":{
							"mode":"form",
							"message":"Need input",
							"requestedSchema":{"type":"object","properties":{}}
						}
					}
				},
				"requestState":"pending"
			}`),
		), nil
	}
	elicitor := &fixtureElicitor{
		elicit: func(
			context.Context,
			mcp.ElicitationRequest,
		) (*mcp.ElicitationResult, error) {
			return nil, errors.New("question service unavailable")
		},
	}
	session := startProtocolTestSession(t, fixture, elicitor)
	_, err := session.callTool(
		t.Context(),
		"needs_input",
		json.RawMessage(`{}`),
	)
	if err == nil || !strings.Contains(err.Error(), "question service unavailable") {
		t.Fatalf("callTool() error = %v", err)
	}
}

func TestProtocolSessionCallToolReturnsTransportErrorWithoutCapture(t *testing.T) {
	transportErr := errors.New("connection lost")
	fixture := &fixtureTransport{}
	fixture.responder = func(
		_ context.Context,
		request transport.JSONRPCRequest,
	) (*transport.JSONRPCResponse, error) {
		if request.Method == string(mcp.MethodServerDiscover) {
			return discoverResponse(request.ID), nil
		}
		return nil, transportErr
	}
	session := startProtocolTestSession(t, fixture, nil)
	_, err := session.callTool(
		t.Context(),
		"disconnect",
		json.RawMessage(`{}`),
	)
	if !errors.Is(err, transportErr) {
		t.Fatalf("callTool() error = %v, want %v", err, transportErr)
	}
}

func TestProtocolTransportAppliesPerRequestTimeoutButNotListenTimeout(t *testing.T) {
	entered := make(chan bool, 2)
	fixture := &fixtureTransport{}
	fixture.responder = func(
		requestContext context.Context,
		_ transport.JSONRPCRequest,
	) (*transport.JSONRPCResponse, error) {
		_, hasDeadline := requestContext.Deadline()
		entered <- hasDeadline
		<-requestContext.Done()
		return nil, requestContext.Err()
	}
	wrapped, err := newProtocolTransport(fixture, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("newProtocolTransport() error = %v", err)
	}
	_, err = wrapped.SendRequest(
		t.Context(),
		transport.JSONRPCRequest{
			JSONRPC: mcp.JSONRPC_VERSION,
			ID:      mcp.NewRequestId(1),
			Method:  string(mcp.MethodToolsList),
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ordinary request error = %v", err)
	}
	if hasDeadline := <-entered; !hasDeadline {
		t.Fatal("ordinary request had no deadline")
	}

	listenContext, cancelListen := context.WithCancel(t.Context())
	listenDone := make(chan error, 1)
	go func() {
		_, listenErr := wrapped.SendRequest(
			listenContext,
			transport.JSONRPCRequest{
				JSONRPC: mcp.JSONRPC_VERSION,
				ID:      mcp.NewRequestId(2),
				Method:  string(mcp.MethodSubscriptionsListen),
			},
		)
		listenDone <- listenErr
	}()
	if hasDeadline := <-entered; hasDeadline {
		t.Fatal("subscriptions/listen inherited the per-request deadline")
	}
	cancelListen()
	if err := <-listenDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("listen request error = %v", err)
	}
}

func TestProtocolSessionCallToolEnforcesResultLimit(t *testing.T) {
	fixture := &fixtureTransport{}
	fixture.responder = func(
		_ context.Context,
		request transport.JSONRPCRequest,
	) (*transport.JSONRPCResponse, error) {
		if request.Method == string(mcp.MethodServerDiscover) {
			return discoverResponse(request.ID), nil
		}
		return transport.NewJSONRPCResultResponse(
			request.ID,
			json.RawMessage(`{"resultType":"complete","content":[{"type":"text","text":"too large"}]}`),
		), nil
	}
	settings := protocolTestSettings(nil)
	settings.toolResultLimit = 16
	session, err := newProtocolSession(fixture, settings)
	if err != nil {
		t.Fatalf("newProtocolSession() error = %v", err)
	}
	if err := session.start(t.Context()); err != nil {
		t.Fatalf("start() error = %v", err)
	}
	t.Cleanup(func() {
		_ = session.close()
	})
	_, err = session.callTool(
		t.Context(),
		"large_result",
		json.RawMessage(`{}`),
	)
	if err == nil || !strings.Contains(err.Error(), "exceeds the configured byte limit") {
		t.Fatalf("callTool() error = %v", err)
	}
}
