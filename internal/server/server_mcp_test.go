package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/tools"
	registryactor "github.com/gorenx/goren/tools/registry/actor"
	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPMCPToolActorElicitationAndCatalogChanges(t *testing.T) {
	testHTTPMCPAgent(t, false)
}

func TestWebHTTPMCPConnectionAndAgentInvocation(t *testing.T) {
	testHTTPMCPAgent(t, true)
}

func testHTTPMCPAgent(t *testing.T, dynamic bool) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	changed := make(chan struct{})
	terminated := make(chan struct{})
	var updated atomic.Bool
	var mcpCalls atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.NewDecoder(request.Body).Decode(&message) != nil {
			t.Error("bad MCP request")
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		respond := func(result string) { fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`, message.ID, result) }
		switch message.Method {
		case "server/discover":
			respond(`{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{"listChanged":true}}}`)
		case "tools/list":
			name := "collect"
			if updated.Load() {
				name = "replacement"
			}
			respond(fmt.Sprintf(`{"resultType":"complete","tools":[{"name":%q,"inputSchema":{"type":"object"}}]}`, name))
		case "subscriptions/listen":
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/subscriptions/acknowledged\",\"params\":{\"notifications\":{\"toolsListChanged\":true},\"_meta\":{\"io.modelcontextprotocol/subscriptionId\":\"sub\"}}}\n\n")
			writer.(http.Flusher).Flush()
			select {
			case <-changed:
				fmt.Fprint(writer, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\",\"params\":{\"_meta\":{\"io.modelcontextprotocol/subscriptionId\":\"sub\"}}}\n\n")
				writer.(http.Flusher).Flush()
			case <-request.Context().Done():
				return
			}
			select {
			case <-terminated:
			case <-request.Context().Done():
			}
		case "tools/call":
			mcpCalls.Add(1)
			if strings.Contains(string(message.Params), `"name":"replacement"`) {
				respond(`{"resultType":"complete","content":[{"type":"text","text":"replacement executed"}]}`)
				return
			}
			if !strings.Contains(string(message.Params), `"requestState":"saved"`) {
				respond(`{"resultType":"input_required","requestState":"saved","inputRequests":{"fields":{"method":"elicitation/create","params":{"mode":"form","message":"Enter the project name","requestedSchema":{"type":"object","properties":{"project":{"type":"string"}},"required":["project"]}}}}}`)
			} else {
				if !strings.Contains(string(message.Params), `"project":"goren"`) {
					t.Errorf("wrong elicitation continuation: %s", message.Params)
				}
				respond(`{"resultType":"complete","content":[{"type":"text","text":"MCP collection succeeded"}]}`)
			}
		default:
			t.Errorf("unexpected MCP method %s", message.Method)
		}
	}))
	defer external.Close()
	var modelCalls atomic.Int32
	modelEndpoint := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input struct {
			Messages json.RawMessage `json:"messages"`
			Tools    json.RawMessage `json:"tools"`
		}
		if json.NewDecoder(request.Body).Decode(&input) != nil {
			t.Error("bad model request")
			return
		}
		step := modelCalls.Add(1)
		name := mcpclient.ToolName("fixture", "collect")
		if step > 2 {
			name = mcpclient.ToolName("fixture", "replacement")
		}
		if !strings.Contains(string(input.Tools), name) {
			t.Errorf("missing MCP schema %s", name)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		if step%2 == 1 {
			fmt.Fprintf(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"mcp-operation-%d\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n", step, name)
		} else {
			if step == 2 && !bytes.Contains(input.Messages, []byte("MCP collection succeeded")) {
				t.Error("MCP result did not reach model")
			}
			if step == 4 && !bytes.Contains(input.Messages, []byte("replacement executed")) {
				t.Error("new dynamic provider did not execute")
			}
			fmt.Fprint(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		}
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer modelEndpoint.Close()
	root := t.TempDir()
	settings := Config{
		Address:           "127.0.0.1:0",
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspaces.sqlite"),
		CredentialsPath:   filepath.Join(root, "credentials.json"),
		Provider: deepseek.Config{
			BaseURL: &modelEndpoint.URL,
		},
		MCP: []mcpclient.Config{
			{
				ServerName: "fixture",
				Transport:  "streamable-http",
				URL:        external.URL,
			},
		},
	}
	if dynamic {
		settings.MCP = nil
	}
	process, err := New(settings, providerEnvironment{
		home: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := process.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	callHTTP(t, ctx, process, "credentials.set", json.RawMessage(`{"ref":"DEEPSEEK_API_KEY","value":"test-key"}`))
	callHTTP(t, ctx, process, "session.create", json.RawMessage(`{"sessionId":"mcp-agent"}`))
	if dynamic {
		initial := callHTTP(t, ctx, process, "mcp.list", json.RawMessage(`{}`))
		if string(initial) != "[]" {
			t.Fatalf("unexpected initial connections: %s", initial)
		}
		request, err := json.Marshal(map[string]string{
			"serverName": "fixture",
			"url":        external.URL,
		})
		if err != nil {
			t.Fatal(err)
		}
		connected := callHTTP(t, ctx, process, "mcp.connect", request)
		if !bytes.Contains(connected, []byte(`"status":"connected"`)) {
			t.Fatalf("MCP did not connect: %s", connected)
		}
	}
	feed, _, err := websocket.Dial(ctx, "ws://"+process.Address()+connection.MuxEventsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer feed.CloseNow()
	callHTTP(t, ctx, process, "session.prompt", json.RawMessage(`{"sessionId":"mcp-agent","mode":"queue","content":[{"type":"text","text":"Call MCP"}]}`))
	questions := 0
	for {
		frame := readConversationFrame(t, ctx, feed)
		if frame.Method == "question/requested" {
			if !bytes.Contains(frame.Payload, []byte(`"sessionId":"mcp-agent"`)) {
				t.Fatalf("wrong Agent question: %s", frame.Payload)
			}
			questions++
			value := json.RawMessage(`{"sessionId":"mcp-agent","answer":{"answers":[{"id":"mcp-consent","selected":["Continue"]}]}}`)
			if questions == 2 {
				value = json.RawMessage(`{"sessionId":"mcp-agent","answer":{"answers":[{"id":"project","selected":[],"custom":"goren"}]}}`)
			}
			if !answerHTTP(t, ctx, process, frame.RPCID, value).Accepted {
				t.Fatal("answer rejected")
			}
		}
		if frame.Method == "session/event" && bytes.Contains(frame.Payload, []byte(`"type":"turn/end"`)) {
			break
		}
	}
	if questions != 2 || mcpCalls.Load() != 2 {
		t.Fatalf("questions=%d MCP rounds=%d", questions, mcpCalls.Load())
	}
	updated.Store(true)
	close(changed)
	directory, err := goakt.GrainOf[*registryactor.RegistryGrain](ctx, process.system, registryactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		payload, err := process.system.AskGrain(ctx, directory, registryactor.ReadConfiguration{
			SessionID: "mcp-agent",
		}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		configuration := payload.(registryactor.Configuration)
		found := false
		for _, entry := range configuration.Value.Global.Schemas {
			if entry.Name == mcpclient.ToolName("fixture", "replacement") {
				found = true
			}
		}
		if found {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("MCP source not refreshed")
		}
	}
	callHTTP(t, ctx, process, "session.prompt", json.RawMessage(`{"sessionId":"mcp-agent","mode":"queue","content":[{"type":"text","text":"Call the new MCP tool"}]}`))
	for {
		frame := readConversationFrame(t, ctx, feed)
		if frame.Method == "session/event" && bytes.Contains(frame.Payload, []byte(`"type":"turn/end"`)) {
			break
		}
	}
	if mcpCalls.Load() != 3 || modelCalls.Load() != 4 {
		t.Fatal("dynamic MCP tool did not complete a real Agent turn")
	}
	if dynamic {
		if _, err := process.mcp.connect(ctx, mcpclient.Config{
			ServerName: "fixture",
			Transport:  "streamable-http",
			URL:        external.URL,
		}); err == nil {
			t.Fatal("duplicate server name silently replaced the active connection")
		}
		close(terminated)
		for {
			listing := callHTTP(t, ctx, process, "mcp.list", json.RawMessage(`{}`))
			payload, err := process.system.AskGrain(ctx, directory, registryactor.ReadConfiguration{
				SessionID: "mcp-agent",
			}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			stale := false
			for _, entry := range payload.(registryactor.Configuration).Value.Global.Schemas {
				stale = stale || strings.HasPrefix(entry.Name, "mcp_")
			}
			if bytes.Contains(listing, []byte(`"status":"failed"`)) && !stale {
				break
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				t.Fatal("subscription termination did not withdraw tools and expose failure")
			}
		}
	}
	callHTTP(t, ctx, process, "mcp.disconnect", json.RawMessage(`{"serverName":"fixture"}`))
	remaining := callHTTP(t, ctx, process, "mcp.list", json.RawMessage(`{}`))
	if string(remaining) != "[]" {
		t.Fatalf("connection remained after disconnect: %s", remaining)
	}
	payload, err := process.system.AskGrain(ctx, directory, registryactor.ReadConfiguration{
		SessionID: "mcp-agent",
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range payload.(registryactor.Configuration).Value.Global.Schemas {
		if strings.HasPrefix(entry.Name, "mcp_") {
			t.Fatal("disconnected tools remained in the Agent catalog")
		}
	}
}

type mcpSource struct {
	result      string
	toolName    string
	entered     chan struct{}
	release     <-chan struct{}
	calls       atomic.Int32
	listens     atomic.Int32
	closed      atomic.Int32
	canceled    atomic.Int32
	unavailable atomic.Bool
}

func (remote *mcpSource) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.NewDecoder(request.Body).Decode(&message) != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if remote.unavailable.Load() {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	respond := func(result string) {
		fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`, message.ID, result)
	}
	switch message.Method {
	case "server/discover":
		respond(`{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{"listChanged":true}}}`)
	case "tools/list":
		name := remote.toolName
		if name == "" {
			name = "read"
		}
		respond(fmt.Sprintf(`{"resultType":"complete","tools":[{"name":%q,"inputSchema":{"type":"object"}}]}`, name))
	case "subscriptions/listen":
		remote.listens.Add(1)
		defer remote.closed.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/subscriptions/acknowledged\",\"params\":{\"notifications\":{\"toolsListChanged\":true},\"_meta\":{\"io.modelcontextprotocol/subscriptionId\":\"sub\"}}}\n\n")
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	case "tools/call":
		remote.calls.Add(1)
		if remote.release != nil {
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, ": waiting\n\n")
			writer.(http.Flusher).Flush()
			remote.entered <- struct{}{}
			select {
			case <-remote.release:
				fmt.Fprintf(writer, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"resultType\":\"complete\",\"content\":[]}}\n\n", message.ID)
			case <-request.Context().Done():
				remote.canceled.Add(1)
			}
			return
		}
		respond(fmt.Sprintf(`{"resultType":"complete","content":[{"type":"text","text":%q}]}`, remote.result))
	default:
		writer.WriteHeader(http.StatusBadRequest)
	}
}

func TestHTTPMCPReconnectionRebindsExistingSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	first := &mcpSource{
		result: "first endpoint result",
	}
	second := &mcpSource{
		result: "second endpoint result",
	}
	firstHTTP := httptest.NewServer(first)
	defer firstHTTP.Close()
	secondHTTP := httptest.NewServer(second)
	defer secondHTTP.Close()
	name := mcpclient.ToolName("fixture", "read")
	var modelCalls atomic.Int32
	modelHTTP := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input struct {
			Messages json.RawMessage `json:"messages"`
			Tools    json.RawMessage `json:"tools"`
		}
		if json.NewDecoder(request.Body).Decode(&input) != nil {
			t.Error("bad model request")
			return
		}
		step := modelCalls.Add(1)
		if !bytes.Contains(input.Tools, []byte(name)) {
			t.Error("MCP tool disappeared during reconnection")
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		if step%2 == 1 {
			fmt.Fprintf(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"read-%d\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n", step, name)
		} else {
			expected := first.result
			if step == 4 {
				expected = second.result
			}
			if !bytes.Contains(input.Messages, []byte(expected)) {
				t.Errorf("endpoint result missing at model call %d", step)
			}
			fmt.Fprint(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		}
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer modelHTTP.Close()
	root := t.TempDir()
	process, err := New(Config{
		Address:           "127.0.0.1:0",
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspaces.sqlite"),
		CredentialsPath:   filepath.Join(root, "credentials.json"),
		Provider: deepseek.Config{
			BaseURL: &modelHTTP.URL,
		},
		MCP: []mcpclient.Config{
			{
				ServerName: "fixture",
				Transport:  "streamable-http",
				URL:        firstHTTP.URL,
			},
		},
	}, providerEnvironment{
		home: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := process.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	callHTTP(t, ctx, process, "credentials.set", json.RawMessage(`{"ref":"DEEPSEEK_API_KEY","value":"test-key"}`))
	callHTTP(t, ctx, process, "session.create", json.RawMessage(`{"sessionId":"mcp-agent"}`))
	feed, _, err := websocket.Dial(ctx, "ws://"+process.Address()+connection.MuxEventsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer feed.CloseNow()
	turn := func() {
		t.Helper()
		callHTTP(t, ctx, process, "session.prompt", json.RawMessage(`{"sessionId":"mcp-agent","mode":"queue","content":[{"type":"text","text":"Read MCP"}]}`))
		for {
			frame := readConversationFrame(t, ctx, feed)
			if frame.Method == "question/requested" {
				if !answerHTTP(t, ctx, process, frame.RPCID, json.RawMessage(`{"sessionId":"mcp-agent","answer":{"answers":[{"id":"mcp-consent","selected":["Continue"]}]}}`)).Accepted {
					t.Fatal("MCP consent rejected")
				}
			}
			if frame.Method == "session/event" && bytes.Contains(frame.Payload, []byte(`"type":"turn/end"`)) {
				return
			}
		}
	}
	directory, err := goakt.GrainOf[*registryactor.RegistryGrain](ctx, process.system, registryactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	binding := func() tools.ProviderID {
		t.Helper()
		payload, err := process.system.AskGrain(ctx, directory, registryactor.ReadConfiguration{
			SessionID: "mcp-agent",
		}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		identifier := payload.(registryactor.Configuration).Value.Global.ProviderIDs[name]
		source, exists := process.mcp.Provider(name)
		identified, valid := source.(tool.IdentifiedProvider)
		if !exists ||
			!valid ||
			identifier == "" {
			t.Fatal("MCP catalog has no identifiable Provider")
		}
		if identifier != identified.ProviderID() {
			t.Fatal("MCP catalog and implementation identities differ")
		}
		return identifier
	}
	previous := binding()
	turn()
	callHTTP(t, ctx, process, "mcp.disconnect", json.RawMessage(`{"serverName":"fixture"}`))
	request, err := json.Marshal(map[string]string{
		"serverName": "fixture",
		"url":        secondHTTP.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	callHTTP(t, ctx, process, "mcp.connect", request)
	if current := binding(); current == previous {
		t.Fatal("new MCP connection reused the old Provider identity")
	}
	turn()
	if first.calls.Load() != 1 ||
		second.calls.Load() != 1 ||
		modelCalls.Load() != 4 {
		t.Fatalf("calls first/second/model=%d/%d/%d", first.calls.Load(), second.calls.Load(), modelCalls.Load())
	}
}
