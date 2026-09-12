package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	sessionevents "github.com/gorenx/goren/apiproxy/session/events"
	settingsapi "github.com/gorenx/goren/apiproxy/settings"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/settings"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
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

func settingsHTTP(t *testing.T, ctx context.Context, process *Server, method string, input any) settingsapi.NamespaceView {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	data := callHTTP(t, ctx, process, method, encoded)
	var view settingsapi.NamespaceView
	if err := json.Unmarshal(data, &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestSettingsHTTPPublishesAndRestoresMCPWithoutOpeningConnections(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	first := &mcpSource{}
	second := &mcpSource{}
	broken := &mcpSource{}
	broken.unavailable.Store(true)
	firstHTTP := httptest.NewServer(first)
	defer firstHTTP.Close()
	secondHTTP := httptest.NewServer(second)
	defer secondHTTP.Close()
	brokenHTTP := httptest.NewServer(broken)
	defer brokenHTTP.Close()
	root := t.TempDir()
	configuration := Config{
		Address:           "127.0.0.1:0",
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspaces.sqlite"),
		CredentialsPath:   filepath.Join(root, "credentials.json"),
		MCP:               []mcpclient.Config{mcpHTTPConfig("fixture", firstHTTP.URL)},
	}
	start := func() *Server {
		t.Helper()
		process, err := New(configuration, providerEnvironment{
			home: root,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Start(ctx); err != nil {
			t.Fatal(err)
		}
		return process
	}
	process := start()
	defer func() {
		if err := process.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	var initial settingsapi.DescribeValue
	if err := json.Unmarshal(callHTTP(t, ctx, process, "settings.describe", json.RawMessage(`{}`)), &initial); err != nil {
		t.Fatal(err)
	}
	if !initial.Writable || len(initial.Namespaces) != 4 {
		t.Fatalf("Settings was not mounted: %+v", initial)
	}
	providerView := settingsHTTP(t, ctx, process, "settings.updateProvider", map[string]any{
		"name":             "deepseek",
		"expectedRevision": 0,
		"config": map[string]any{
			"apiKeyEnv": "SETTINGS_TEST_API_KEY",
		},
	})
	if !providerView.PendingRestart || providerView.Applies != settingsapi.ApplyRestart {
		t.Fatal("Provider edit falsely reported live application")
	}
	save := func(expected int64, definition mcpclient.Config, enabled bool) settingsapi.NamespaceView {
		return settingsHTTP(t, ctx, process, "settings.updateMCP", settingsapi.UpdateMCPRequest{
			Name:             definition.ServerName,
			ExpectedRevision: expected,
			Config: settings.MCPConfig{
				Enabled:    enabled,
				Connection: definition,
			},
		})
	}
	if first.listens.Load() != 0 {
		t.Fatal("Server opened MCP before a Session existed")
	}
	target := mcpHTTPConfig("fixture", secondHTTP.URL)
	view := save(0, target, true)
	fixtureRevision := view.Revision
	if view.ApplyError != "" || view.AppliedRevision == nil || *view.AppliedRevision != view.Revision {
		t.Fatalf("MCP configuration was not published: %+v", view)
	}
	if definitions := mcpDefinitions(t, ctx, process); len(definitions) != 1 || definitions["fixture"].URL != secondHTTP.URL {
		t.Fatal("Settings HTTP did not publish the replacement target")
	}
	view = save(0, mcpHTTPConfig("broken", brokenHTTP.URL), true)
	if view.ApplyError != "" || view.AppliedRevision == nil || *view.AppliedRevision != view.Revision {
		t.Fatal("unavailable endpoint prevented configuration publication")
	}
	retried := settingsHTTP(t, ctx, process, "settings.retryApplication", map[string]any{
		"ns":               "mcp",
		"name":             "broken",
		"expectedRevision": view.Revision,
	})
	if retried.Revision != view.Revision || retried.ApplyError != "" || *retried.AppliedRevision != retried.Revision {
		t.Fatalf("configuration publication retry=%+v", retried)
	}
	if first.listens.Load() != 0 || second.listens.Load() != 0 || broken.listens.Load() != 0 {
		t.Fatal("Settings acquired network connections")
	}
	if err := process.Close(ctx); err != nil {
		t.Fatal(err)
	}
	process = start()
	definitions := mcpDefinitions(t, ctx, process)
	if len(definitions) != 2 || definitions["fixture"].URL != secondHTTP.URL || definitions["broken"].URL != brokenHTTP.URL {
		t.Fatal("restart did not publish the persisted HTTP targets")
	}
	if process.settings.Provider.APIKeyEnv == nil || *process.settings.Provider.APIKeyEnv != "SETTINGS_TEST_API_KEY" {
		t.Fatal("restart did not apply persisted Provider configuration")
	}
	view = settingsHTTP(t, ctx, process, "settings.resetMCP", map[string]any{
		"expectedRevision": retried.Revision,
		"name":             "broken",
	})
	if view.ApplyError != "" || *view.AppliedRevision != view.Revision || len(mcpDefinitions(t, ctx, process)) != 1 {
		t.Fatalf("reset did not publish the remaining configuration: %+v", view)
	}
	view = settingsHTTP(t, ctx, process, "settings.resetMCP", settingsapi.ResetMCPRequest{
		Name:             "fixture",
		ExpectedRevision: fixtureRevision,
	})
	if view.ApplyError != "" || mcpDefinitions(t, ctx, process)["fixture"].URL != firstHTTP.URL {
		t.Fatal("reset did not restore the local HTTP baseline")
	}
	view = save(view.Revision, mcpHTTPConfig("fixture", firstHTTP.URL), false)
	if view.ApplyError != "" || len(mcpDefinitions(t, ctx, process)) != 0 {
		t.Fatal("disabled named HTTP configuration was not published")
	}
	if err := process.Close(ctx); err != nil {
		t.Fatal(err)
	}
	process = start()
	if len(mcpDefinitions(t, ctx, process)) != 0 || first.listens.Load() != 0 || second.listens.Load() != 0 || broken.listens.Load() != 0 {
		t.Fatal("restart discarded disabled configuration or opened a network connection")
	}

}

func TestPromptBaselineUpdateReachesExistingSessionNextTurn(t *testing.T) {
	requests := make(chan string, 4)
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		var request struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(incoming.Body).Decode(&request); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, entry := range request.Messages {
			if entry.Role == "system" && strings.Contains(string(entry.Content), "configuration-persona-") {
				requests <- string(entry.Content)
			}
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer remote.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	process, err := New(Config{
		Address:           "127.0.0.1:0",
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspace.sqlite"),
		CredentialsPath:   filepath.Join(root, "credentials.json"),
		Provider: deepseek.Config{
			BaseURL: &remote.URL,
		},
		Prompt: systemprompt.Config{
			Persona: "configuration-persona-old",
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
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if err := process.Close(cleanup); err != nil {
			t.Error(err)
		}
	}()
	callHTTP(t, ctx, process, "credentials.set", json.RawMessage(`{"ref":"DEEPSEEK_API_KEY","value":"test-key"}`))
	callHTTP(t, ctx, process, "session.create", json.RawMessage(`{"sessionId":"prompt-settings"}`))
	feed, _, err := websocket.Dial(ctx, "ws://"+process.Address()+connection.MuxEventsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer feed.CloseNow()
	baseline := readConversationFrame(t, ctx, feed)
	if baseline.Method != "session/subscribed" {
		t.Fatal("subscription baseline missing")
	}
	var revision int64
	for index, marker := range []string{"configuration-persona-old", "configuration-persona-new", "configuration-persona-old"} {
		if index == 1 {
			payload := callHTTP(t, ctx, process, "settings.updatePrompt", json.RawMessage(`{"name":"default","expectedRevision":0,"config":{"persona":"configuration-persona-new"}}`))
			var view settingsapi.NamespaceView
			if err := json.Unmarshal(payload, &view); err != nil {
				t.Fatal(err)
			}
			if view.ApplyError != "" ||
				view.AppliedRevision == nil ||
				*view.AppliedRevision != view.Revision {
				t.Fatalf("Prompt save did not apply: %s", payload)
			}
			revision = view.Revision
		}
		if index == 2 {
			callHTTP(t, ctx, process, "settings.resetPrompt", json.RawMessage(fmt.Sprintf(`{"name":"default","expectedRevision":%d}`, revision)))
		}
		callHTTP(t, ctx, process, "session.prompt", json.RawMessage(`{"sessionId":"prompt-settings","mode":"queue","content":[{"type":"text","text":"hello"}]}`))
		select {
		case actual := <-requests:
			if !strings.Contains(actual, marker) {
				t.Fatalf("Turn %d received stale system prompt: %s", index, actual)
			}
		case <-ctx.Done():
			t.Fatal("configuration never reached the model request")
		}
		for {
			frame := readConversationFrame(t, ctx, feed)
			if frame.Method != "session/event" {
				continue
			}
			var notice sessionevents.EventFrame
			if err := json.Unmarshal(frame.Payload, &notice); err != nil {
				t.Fatal(err)
			}
			if notice.SessionID == "prompt-settings" && notice.Event.Type == session.TurnEndEventName {
				break
			}
		}
	}
}

func TestSettingsHTTPToolUpdatePreservesTurnAndSurvivesRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	source := &echoProvider{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}, 1),
	}
	requests := make(chan bool, 4)
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(incoming.Body).Decode(&body); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		visible := false
		for _, entry := range body.Tools {
			if entry.Function.Name == "echo" {
				visible = true
			}
		}
		requests <- visible
		writer.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			fmt.Fprint(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"echo-1\",\"type\":\"function\",\"function\":{\"name\":\"echo\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		} else {
			fmt.Fprint(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		}
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer remote.Close()
	root := t.TempDir()
	configuration := Config{
		Address:           "127.0.0.1:0",
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspace.sqlite"),
		CredentialsPath:   filepath.Join(root, "credentials.json"),
		Provider: deepseek.Config{
			BaseURL: &remote.URL,
		},
		Tools: toolregistry.Catalog{
			Schemas: []llm.ToolSchema{echoSchema("echo settings")},
		},
		Providers: tool.Providers{"echo": source},
	}
	start := func() *Server {
		t.Helper()
		process, err := New(configuration, providerEnvironment{
			home: root,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Start(ctx); err != nil {
			t.Fatal(err)
		}
		return process
	}
	process := start()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := process.Close(cleanup); err != nil {
			t.Error(err)
		}
	}()
	callHTTP(t, ctx, process, "credentials.set", json.RawMessage(`{"ref":"DEEPSEEK_API_KEY","value":"test-key"}`))
	callHTTP(t, ctx, process, "session.create", json.RawMessage(`{"sessionId":"tool-settings"}`))
	feed, _, err := websocket.Dial(ctx, "ws://"+process.Address()+connection.MuxEventsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer feed.CloseNow()
	if readConversationFrame(t, ctx, feed).Method != "session/subscribed" {
		t.Fatal("missing subscription baseline")
	}
	prompt := func() {
		t.Helper()
		callHTTP(t, ctx, process, "session.prompt", json.RawMessage(`{"sessionId":"tool-settings","mode":"queue","content":[{"type":"text","text":"hello"}]}`))
	}
	waitTurn := func() {
		t.Helper()
		for {
			frame := readConversationFrame(t, ctx, feed)
			if frame.Method != "session/event" {
				continue
			}
			var notice sessionevents.EventFrame
			if err := json.Unmarshal(frame.Payload, &notice); err != nil {
				t.Fatal(err)
			}
			if notice.SessionID == "tool-settings" && notice.Event.Type == session.TurnEndEventName {
				return
			}
		}
	}
	assertRequest := func(want bool) {
		t.Helper()
		select {
		case actual := <-requests:
			if actual != want {
				t.Fatalf("model received echo=%v, want %v", actual, want)
			}
		case <-ctx.Done():
			t.Fatal("model request missing")
		}
	}
	prompt()
	select {
	case <-source.entered:
	case <-ctx.Done():
		t.Fatal("tool did not begin")
	}
	disabled := false
	view := settingsHTTP(t, ctx, process, settingsapi.UpdateToolMethod, settingsapi.UpdateToolRequest{
		Name: "echo",
		Config: tools.Config{
			Enabled: &disabled,
			Visible: &disabled,
		},
	})
	if view.ApplyError != "" ||
		view.AppliedRevision == nil ||
		*view.AppliedRevision != view.Revision {
		t.Fatalf("tool configuration was not applied: %+v", view)
	}
	if source.opens.Load() != 1 || source.closes.Load() != 0 {
		t.Fatal("configuration interrupted the active tool")
	}
	source.release <- struct{}{}
	waitTurn()
	assertRequest(true)
	assertRequest(true)
	prompt()
	waitTurn()
	assertRequest(false)
	if source.closes.Load() != 1 {
		t.Fatal("disabled tool was not released at next Turn")
	}
	feed.CloseNow()
	if err := process.Close(ctx); err != nil {
		t.Fatal(err)
	}
	process = start()
	directory, err := goakt.GrainOf[*catalogactor.RegistryGrain](ctx, process.system, catalogactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	read := runtimeRequest[catalogactor.Configuration](t, ctx, process.system, directory, catalogactor.ReadConfiguration{})
	policy := read.Value.Global.Policies["echo"]
	if policy.Enabled == nil ||
		*policy.Enabled ||
		policy.Visible == nil ||
		*policy.Visible {
		t.Fatal("restart lost saved tool configuration")
	}
	reset := settingsHTTP(t, ctx, process, settingsapi.ResetToolMethod, settingsapi.ResetToolRequest{
		Name:             "echo",
		ExpectedRevision: view.Revision,
	})
	if reset.User != nil || reset.ApplyError != "" {
		t.Fatalf("reset did not restore inheritance: %+v", reset)
	}
	read = runtimeRequest[catalogactor.Configuration](t, ctx, process.system, directory, catalogactor.ReadConfiguration{})
	policy = read.Value.Global.Policies["echo"]
	if policy.Enabled == nil ||
		!*policy.Enabled ||
		policy.Visible == nil ||
		!*policy.Visible {
		t.Fatal("reset did not apply the local tool group")
	}
}

func mcpDefinitions(t *testing.T, ctx context.Context, process *Server) map[string]mcpclient.Config {
	t.Helper()
	address, err := goakt.GrainOf[*catalogactor.RegistryGrain](ctx, process.system, catalogactor.RegistryName)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := process.system.AskGrain(ctx, address, catalogactor.ReadConfiguration{
		SessionID: "configuration-probe",
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result := payload.(catalogactor.Configuration)
	definitions := make(map[string]mcpclient.Config)
	for _, definition := range result.Value.Global.MCP {
		definitions[definition.ServerName] = definition
	}
	return definitions
}
