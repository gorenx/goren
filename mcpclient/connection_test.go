package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/userquestions"
)

// These fixtures encode the wire independently of the SDK server. A successful
// SDK round trip alone would not detect matching client/server protocol defects.
type wireRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

const fixtureAck = `{"jsonrpc":"2.0","method":"notifications/subscriptions/acknowledged","params":{"notifications":{"toolsListChanged":true},"_meta":{"io.modelcontextprotocol/subscriptionId":"fixture-subscription"}}}`
const fixtureChanged = `{"jsonrpc":"2.0","method":"notifications/tools/list_changed","params":{"_meta":{"io.modelcontextprotocol/subscriptionId":"fixture-subscription"}}}`
const fixtureDiscover = `{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{"listChanged":true}}}`

func listFixture(cursor string) string {
	if cursor == "" {
		return `{"resultType":"complete","tools":[{"name":"form","inputSchema":{"type":"object"}}],"nextCursor":"page-2"}`
	}
	return `{"resultType":"complete","tools":[{"name":"url","inputSchema":{"type":"object"}},{"name":"wait","inputSchema":{"type":"object"}}]}`
}

func roundFixture(params json.RawMessage) string {
	var input struct {
		Name           string          `json:"name"`
		Arguments      map[string]any  `json:"arguments"`
		RequestState   string          `json:"requestState"`
		InputResponses json.RawMessage `json:"inputResponses"`
	}
	_ = json.Unmarshal(params, &input)
	if input.RequestState != "" {
		encoded, _ := json.Marshal(map[string]any{
			"resultType":        "complete",
			"content":           []any{map[string]any{"type": "text", "text": "completed"}},
			"structuredContent": map[string]any{"state": input.RequestState, "answers": input.InputResponses, "arguments": input.Arguments},
		})
		return string(encoded)
	}
	if input.Name == "form" {
		return `{"resultType":"input_required","requestState":"form-state","inputRequests":{"input":{"method":"elicitation/create","params":{"mode":"form","message":"Enter a count","requestedSchema":{"type":"object","properties":{"count":{"type":"integer","minimum":1}},"required":["count"],"additionalProperties":false}}}}}`
	}
	return `{"resultType":"input_required","requestState":"url-state","inputRequests":{"input":{"method":"elicitation/create","params":{"mode":"url","message":"Open the external workflow","url":"https://example.com/authorize?flow=test","elicitationId":"external-flow"}}}}`
}

type answeringProvider struct {
	mutex    sync.Mutex
	requests []userquestions.Request
}

func (answerer *answeringProvider) Ask(_ context.Context, input userquestions.Request) (userquestions.Answer, error) {
	answerer.mutex.Lock()
	answerer.requests = append(answerer.requests, input)
	answerer.mutex.Unlock()
	if len(input.Questions) != 1 {
		return userquestions.Answer{}, errors.New("unexpected question batch")
	}
	question := input.Questions[0]
	if question.ID == "mcp-consent" {
		return userquestions.Answer{
			Answers: []userquestions.AnswerItem{
				{
					ID:       question.ID,
					Selected: []string{"Continue"},
				},
			},
		}, nil
	}
	count := "7"
	return userquestions.Answer{
		Answers: []userquestions.AnswerItem{
			{
				ID:     question.ID,
				Custom: &count,
			},
		},
	}, nil
}

func fixtureExecution(t *testing.T, remote *Connection, ctx context.Context, rawName string, agentID session.SessionID) tools.BodyResult {
	t.Helper()
	name := ToolName("fixture", rawName)
	source, exists := remote.Provider(name)
	if !exists {
		t.Fatalf("missing provider %s", name)
	}
	implementation, err := source.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return (tools.Invocation{
		Name:      name,
		SessionID: agentID,
		CallID:    agentmessage.CallID("call-" + string(agentID)),
		Arguments: json.RawMessage(`{"original":true}`),
	}).Run(ctx, implementation.Definition.Executor)
}

func assertRouting(t *testing.T, answerer *answeringProvider) {
	t.Helper()
	answerer.mutex.Lock()
	defer answerer.mutex.Unlock()
	if len(answerer.requests) != 3 {
		t.Fatalf("questions = %d", len(answerer.requests))
	}
	for _, request := range answerer.requests {
		question := request.Questions[0]
		isURL := question.Detail != nil && strings.Contains(*question.Detail, "URL:")
		if isURL && request.SessionID != "agent-url" {
			t.Fatal("URL routed to wrong Agent")
		}
		if !isURL && request.SessionID != "agent-form" {
			t.Fatal("form routed to wrong Agent")
		}
	}
}

func TestHTTPConnectionPaginationMRTRRoutingAndStreamLifetime(t *testing.T) {
	for _, skip := range []bool{false, true} {
		t.Run(fmt.Sprint("skip-discover=", skip), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			finishSubscription := make(chan struct{})
			callEntered := make(chan struct{})
			callClosed := make(chan struct{})
			var mutex sync.Mutex
			var methods []string
			remoteServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodPost {
					t.Errorf("unexpected HTTP %s", request.Method)
					writer.WriteHeader(405)
					return
				}
				var message wireRequest
				if json.NewDecoder(request.Body).Decode(&message) != nil {
					t.Error("invalid request")
					return
				}
				mutex.Lock()
				methods = append(methods, message.Method)
				mutex.Unlock()
				if request.Header.Get("Mcp-Protocol-Version") != protocolVersion ||
					request.Header.Get("Mcp-Method") != message.Method ||
					request.Header.Get("Mcp-Session-Id") != "" {
					t.Error("incorrect MCP headers")
				}
				if !strings.Contains(string(message.Params), `"io.modelcontextprotocol/protocolVersion":"2026-07-28"`) {
					t.Errorf("missing metadata: %s", message.Params)
				}
				respond := func(result string) { fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`, message.ID, result) }
				writer.Header().Set("Content-Type", "application/json")
				switch message.Method {
				case "server/discover":
					respond(fixtureDiscover)
				case "tools/list":
					var params struct {
						Cursor string `json:"cursor"`
					}
					_ = json.Unmarshal(message.Params, &params)
					respond(listFixture(params.Cursor))
				case "subscriptions/listen":
					writer.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(writer, "data: %s\n\ndata: %s\n\n", fixtureAck, fixtureChanged)
					writer.(http.Flusher).Flush()
					select {
					case <-finishSubscription:
						fmt.Fprintf(writer, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"resultType\":\"complete\"}}\n\n", message.ID)
					case <-request.Context().Done():
					}
				case "tools/call":
					var params struct {
						Name string `json:"name"`
					}
					_ = json.Unmarshal(message.Params, &params)
					if params.Name == "wait" {
						writer.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(writer, ": waiting\n\n")
						writer.(http.Flusher).Flush()
						close(callEntered)
						<-request.Context().Done()
						close(callClosed)
						return
					}
					// Both JSON and request-scoped SSE responses drive the same SDK MRTR.
					if params.Name == "url" {
						writer.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(writer, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":%s}\n\n", message.ID, roundFixture(message.Params))
					} else {
						respond(roundFixture(message.Params))
					}
				default:
					t.Errorf("unexpected method %s", message.Method)
				}
			}))
			defer remoteServer.Close()
			answerer := &answeringProvider{}
			remote, err := Open(ctx, Config{
				ServerName:   "fixture",
				Transport:    "streamable-http",
				URL:          remoteServer.URL,
				SkipDiscover: skip,
			}, answerer)
			if err != nil {
				t.Fatal(err)
			}
			defer remote.Close(context.Background())
			if len(remote.Schemas()) != 3 {
				t.Fatal("pagination did not complete")
			}
			select {
			case <-remote.Changes():
			case <-ctx.Done():
				t.Fatal("missing tools change")
			}
			results := make(chan tools.BodyResult, 2)
			for _, rawName := range []string{"form", "url"} {
				go func() { results <- fixtureExecution(t, remote, ctx, rawName, session.SessionID("agent-"+rawName)) }()
			}
			for range 2 {
				result := <-results
				if result.Err != nil ||
					!strings.Contains(string(result.Value), `"action":"accept"`) ||
					!strings.Contains(string(result.Value), `"original":true`) {
					t.Fatalf("MRTR result = %s, %v", result.Value, result.Err)
				}
			}
			assertRouting(t, answerer)
			work, abort := context.WithCancel(ctx)
			go func() { results <- fixtureExecution(t, remote, work, "wait", "agent-wait") }()
			select {
			case <-callEntered:
			case <-ctx.Done():
				t.Fatal("call did not start")
			}
			abort()
			if result := <-results; !errors.Is(result.Err, context.Canceled) {
				t.Fatalf("cancel result: %v", result.Err)
			}
			select {
			case <-callClosed:
			case <-ctx.Done():
				t.Fatal("HTTP response stream not closed")
			}
			close(finishSubscription)
			select {
			case <-remote.Done():
			case <-ctx.Done():
				t.Fatal("subscription end not observed")
			}
			if remote.Cause() == nil {
				t.Fatal("successful subscription completion was treated as healthy")
			}
			mutex.Lock()
			defer mutex.Unlock()
			if slices.Contains(methods, "server/discover") == skip {
				t.Fatal("optional discovery not respected")
			}
		})
	}
}

func TestStdioFixtureProcess(t *testing.T) {
	if os.Getenv("GOREN_MCP_FIXTURE") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	var waiting json.RawMessage
	for scanner.Scan() {
		var message wireRequest
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			os.Exit(2)
		}
		respond := func(result string) {
			fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":%s}\n", message.ID, result)
		}
		switch message.Method {
		case "server/discover":
			respond(fixtureDiscover)
		case "subscriptions/listen":
			fmt.Fprintln(os.Stdout, fixtureAck)
		case "tools/list":
			var params struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(message.Params, &params)
			respond(listFixture(params.Cursor))
			if params.Cursor != "" && os.Getenv("GOREN_MCP_BLOCK_STDIN") == "1" {
				_, _ = os.Stdin.Read(make([]byte, 1))
				fmt.Fprintln(os.Stdout, fixtureChanged)
				time.Sleep(time.Hour)
			}
		case "tools/call":
			if strings.Contains(string(message.Params), `"name":"wait"`) {
				waiting = message.ID
			} else {
				respond(roundFixture(message.Params))
			}
		case "notifications/cancelled":
			var params struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			_ = json.Unmarshal(message.Params, &params)
			if string(params.RequestID) == string(waiting) {
				fmt.Fprintln(os.Stdout, fixtureChanged)
			}
		default:
			os.Exit(3)
		}
	}
	os.Exit(0)
}

func TestStdioCancellationClosesBlockedPipeWriter(t *testing.T) {
	t.Setenv("GOREN_MCP_FIXTURE", "1")
	t.Setenv("GOREN_MCP_BLOCK_STDIN", "1")
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	remote, err := Open(ctx, Config{
		ServerName: "fixture",
		Transport:  "stdio",
		Command:    executable,
		Args:       []string{"-test.run=^TestStdioFixtureProcess$"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close(context.Background())
	work, abort := context.WithCancel(ctx)
	defer abort()
	arguments, _ := json.Marshal(map[string]string{"large": strings.Repeat("x", 2<<20)})
	returned := make(chan error, 1)
	go func() {
		_, failure := remote.session.callTool(work, "wait", arguments)
		returned <- failure
	}()
	select {
	case <-remote.Changes():
	case <-ctx.Done():
		t.Fatal("child did not observe the first request byte")
	}
	abort()
	select {
	case failure := <-returned:
		if failure == nil {
			t.Fatal("blocked pipe call succeeded")
		}
	case <-ctx.Done():
		t.Fatal("cancelled writer did not return")
	}
	select {
	case <-remote.Done():
	case <-ctx.Done():
		t.Fatal("blocked SDK writer did not settle on cancellation")
	}
}

func TestStdioConnectionMRTRAndCallerCancellation(t *testing.T) {
	t.Setenv("GOREN_MCP_FIXTURE", "1")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	answerer := &answeringProvider{}
	remote, err := Open(ctx, Config{
		ServerName: "fixture",
		Transport:  "stdio",
		Command:    executable,
		Args:       []string{"-test.run=^TestStdioFixtureProcess$"},
	}, answerer)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close(context.Background())
	for _, rawName := range []string{"form", "url"} {
		result := fixtureExecution(t, remote, ctx, rawName, session.SessionID("agent-"+rawName))
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	assertRouting(t, answerer)
	work, abort := context.WithTimeout(ctx, 40*time.Millisecond)
	defer abort()
	result := fixtureExecution(t, remote, work, "wait", "agent-wait")
	if !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("cancellation = %v", result.Err)
	}
	select {
	case <-remote.Changes():
	case <-ctx.Done():
		t.Fatal("stdio did not receive cancellation for the exact request ID")
	}
	if err := remote.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := remote.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionShutdownCancelsPendingUserQuestion(t *testing.T) {
	t.Setenv("GOREN_MCP_FIXTURE", "1")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	remote, err := Open(ctx, Config{
		ServerName: "fixture",
		Transport:  "stdio",
		Command:    executable,
		Args:       []string{"-test.run=^TestStdioFixtureProcess$"},
	}, &questionFixture{
		ask: func(ctx context.Context, _ userquestions.Request) (userquestions.Answer, error) {
			close(entered)
			<-ctx.Done()
			return userquestions.Answer{}, ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close(context.Background())
	returned := make(chan tools.BodyResult, 1)
	go func() { returned <- fixtureExecution(t, remote, ctx, "form", "agent-wait") }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("question not reached")
	}
	if err := remote.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-returned:
		if !errors.Is(outcome.Err, context.Canceled) {
			t.Fatalf("pending call not cancelled: %v", outcome.Err)
		}
	case <-ctx.Done():
		t.Fatal("Close returned without draining the tool")
	}
}
