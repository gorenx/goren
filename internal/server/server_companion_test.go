package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	agentregistry "github.com/gorenx/goren/agent/registry"
	commandsapi "github.com/gorenx/goren/apiproxy/commands"
	companionapi "github.com/gorenx/goren/apiproxy/companion"
	settingsapi "github.com/gorenx/goren/apiproxy/settings"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/settings"
	companions "github.com/gorenx/goren/subagent/companion"
	companionactor "github.com/gorenx/goren/subagent/companion/actor"
	"github.com/gorenx/goren/subagent/tools/control"
	"github.com/gorenx/goren/tools"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func TestServerInstallsDefaultCompanionForEachParent(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	configuration := Config{
		Address:           "127.0.0.1:0",
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspaces.sqlite"),
		CredentialsPath:   filepath.Join(root, "credentials.json"),
		Agents: map[string]settings.Values{
			"review-config": {
				Prompts: map[string]settings.PromptConfig{
					"default": {
						Persona: new("review independently"),
					},
				},
			},
		},
		Companions: map[string]companions.Config{
			"reviewer": {
				Enabled:       true,
				Configuration: "review-config",
			},
		},
	}
	process, err := New(configuration, providerEnvironment{
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
	observer, err := companionactor.NewReader(process.system, process.agents)
	if err != nil {
		t.Fatal(err)
	}
	children := make(map[string]string)
	for _, parentID := range []string{"first-parent", "second-parent"} {
		payload, err := json.Marshal(struct {
			SessionID string `json:"sessionId"`
		}{
			SessionID: parentID,
		})
		if err != nil {
			t.Fatal(err)
		}
		callHTTP(t, ctx, process, "session.create", payload)
		ready := serverReady(t, ctx, process, parentID)
		if len(ready.Components) != 2 {
			t.Fatalf("parent %s missing Companion installation: %+v", parentID, ready.Components)
		}
		address, err := goakt.GrainOf[*runtimeactor.Runtime](ctx, process.system, runtimeactor.RuntimeName(parentID))
		if err != nil {
			t.Fatal(err)
		}
		defined, err := process.system.AskGrain(ctx, address, runtimeactor.ReadDefinitions{
			CreationID: ready.Creation.CreationID,
		}, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		definitions, ok := defined.(runtimeactor.Definitions)
		if !ok || definitions.Err != nil || !containsName(definitions.Value.KnownNames, control.ListSchema().Name) || !containsName(definitions.Value.KnownNames, control.MessageSchema().Name) {
			t.Fatalf("parent %s has no control tools: %+v", parentID, defined)
		}
		credential := tools.WithAgentCreationID(ctx, ready.Creation.CreationID)
		var rows []control.Entry
		for {
			rows, err = observer.List(credential, control.Listing{
				ParentSessionID: ready.Creation.AgentID,
				Scope:           "children",
			})
			if err == nil && len(rows) == 1 && rows[0].Status == "idle" {
				break
			}
			select {
			case <-time.After(5 * time.Millisecond):
			case <-ctx.Done():
				t.Fatalf("parent %s default child did not become ready: rows=%+v err=%v", parentID, rows, err)
			}
		}
		if rows[0].Label != "reviewer" || rows[0].ID == "" || rows[0].ID == parentID {
			t.Fatalf("parent %s has incorrect child: %+v", parentID, rows)
		}
		children[parentID] = rows[0].ID
		child := serverReady(t, ctx, process, rows[0].ID)
		if len(child.Components) != 1 {
			t.Fatalf("Companion %s inherited the Installer: %+v", child.Creation.AgentID, child.Components)
		}
		rename, err := json.Marshal(struct {
			SessionID string `json:"sessionId"`
			Title     string `json:"title"`
		}{
			SessionID: rows[0].ID,
			Title:     "Companion " + parentID,
		})
		if err != nil {
			t.Fatal(err)
		}
		callHTTP(t, ctx, process, "session.rename", rename)
		commands, err := json.Marshal(struct {
			Args struct {
				AgentID string `json:"agentId"`
			} `json:"args"`
		}{
			Args: struct {
				AgentID string `json:"agentId"`
			}{
				AgentID: rows[0].ID,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		callHTTP(t, ctx, process, commandsapi.ListMethod, commands)
		if result := companionResult(t, ctx, process, "session.create", payloadForSession(t, rows[0].ID)); result.OK || result.Error == nil || result.Error.Code != connection.ErrorAgentBusy {
			t.Fatalf("ordinary creation adopted Companion Session: %+v", result)
		}
	}
	if children["first-parent"] == children["second-parent"] {
		t.Fatal("two parents share one Companion Session")
	}
	if err := process.Close(ctx); err != nil {
		t.Fatal(err)
	}
	process, err = New(configuration, providerEnvironment{
		home: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Start(ctx); err != nil {
		t.Fatal(err)
	}
	observer, err = companionactor.NewReader(process.system, process.agents)
	if err != nil {
		t.Fatal(err)
	}
	for parentID, originalChild := range children {
		payload, err := json.Marshal(struct {
			SessionID string `json:"sessionId"`
		}{
			SessionID: parentID,
		})
		if err != nil {
			t.Fatal(err)
		}
		callHTTP(t, ctx, process, "session.create", payload)
		parent := serverReady(t, ctx, process, parentID)
		credential := tools.WithAgentCreationID(ctx, parent.Creation.CreationID)
		for {
			rows, listErr := observer.List(credential, control.Listing{
				ParentSessionID: parent.Creation.AgentID,
				Scope:           "children",
			})
			if listErr == nil && len(rows) == 1 && rows[0].ID == originalChild && rows[0].Status == "idle" {
				break
			}
			select {
			case <-time.After(5 * time.Millisecond):
			case <-ctx.Done():
				t.Fatalf("parent %s did not restore child %s: rows=%+v err=%v", parentID, originalChild, rows, listErr)
			}
		}
	}
}

func TestCompanionMCPConnectionsRemainSessionScoped(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var call struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(request.Body).Decode(&call); err != nil {
			t.Errorf("decode MCP request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch call.Method {
		case "server/discover":
			fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{"listChanged":true}}}}`, call.ID)
		case "tools/list":
			fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","tools":[{"name":"collect","inputSchema":{"type":"object"}}]}}`, call.ID)
		case "subscriptions/listen":
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/subscriptions/acknowledged\",\"params\":{\"notifications\":{\"toolsListChanged\":true},\"_meta\":{\"io.modelcontextprotocol/subscriptionId\":\"sub\"}}}\n\n")
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
		default:
			t.Errorf("unexpected MCP method: %s", call.Method)
			writer.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer remote.Close()
	root := t.TempDir()
	process, err := New(Config{
		Address:           "127.0.0.1:0",
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspaces.sqlite"),
		CredentialsPath:   filepath.Join(root, "credentials.json"),
		Agents: map[string]settings.Values{
			"mcp-child": {
				MCP: map[string]settings.MCPConfig{
					"fixture": {
						Enabled: true,
						Connection: mcpclient.Config{
							ServerName: "fixture",
							Transport:  "streamable-http",
							URL:        remote.URL,
						},
					},
				},
			},
		},
		Companions: map[string]companions.Config{
			"helper": {
				Enabled:       true,
				Configuration: "mcp-child",
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
	providers := make(map[string]tools.ProviderID)
	for _, parentID := range []string{"first-parent", "second-parent"} {
		callHTTP(t, ctx, process, "session.create", payloadForSession(t, parentID))
		var child companionapi.View
		for {
			result := callHTTP(t, ctx, process, companionapi.ListMethod, json.RawMessage(`{"parentSessionId":"`+parentID+`"}`))
			var listing companionapi.Listing
			if err := json.Unmarshal(result, &listing); err != nil {
				t.Fatal(err)
			}
			if listing.Phase == "ready" && len(listing.Items) == 1 && listing.Items[0].Phase == "ready" {
				child = listing.Items[0]
				break
			}
			select {
			case <-time.After(5 * time.Millisecond):
			case <-ctx.Done():
				t.Fatalf("Companion MCP child did not start for %s: %+v", parentID, listing)
			}
		}
		name := mcpclient.ToolName("fixture", "collect")
		identifier := readMCPRuntime(t, ctx, process, child.ChildSessionID).ProviderIDs[name]
		if identifier == "" {
			t.Fatalf("Companion %s did not receive MCP tool %s", child.ChildSessionID, name)
		}
		providers[parentID] = identifier
	}
	if providers["first-parent"] == providers["second-parent"] {
		t.Fatal("two Companion Sessions shared one MCP Provider binding")
	}
	if initial := readMCPRuntime(t, ctx, process, "first-parent").ProviderIDs[mcpclient.ToolName("fixture", "collect")]; initial != "" {
		t.Fatal("Companion MCP tool leaked into parent Session")
	}
}

func serverReady(t *testing.T, ctx context.Context, process *Server, identifier string) agentregistry.Ready {
	t.Helper()
	payload, err := process.system.AskGrain(ctx, process.agents, agentregistry.FindAgent{
		AgentID: session.SessionID(identifier),
	}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ready, ok := payload.(agentregistry.Ready)
	if !ok || ready.Err != nil {
		t.Fatalf("Agent %s is not ready: %+v", identifier, payload)
	}
	return ready
}

func containsName(entries []string, name string) bool {
	for _, entry := range entries {
		if entry == name {
			return true
		}
	}
	return false
}

func payloadForSession(t *testing.T, identifier string) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(struct {
		SessionID string `json:"sessionId"`
	}{
		SessionID: identifier,
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestCompanionManagementHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	configuration := Config{
		Address:           "127.0.0.1:0",
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspaces.sqlite"),
		CredentialsPath:   filepath.Join(root, "credentials.json"),
		Agents: map[string]settings.Values{
			"review-config": {
				Prompts: map[string]settings.PromptConfig{
					"default": {
						Persona: new("review independently"),
					},
				},
			},
		},
	}
	process, err := New(configuration, providerEnvironment{
		home: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	callHTTP(t, ctx, process, "session.create", json.RawMessage(`{"sessionId":"managed-parent"}`))
	read := func() companionapi.Listing {
		t.Helper()
		var listing companionapi.Listing
		encoded := callHTTP(t, ctx, process, companionapi.ListMethod, json.RawMessage(`{"parentSessionId":"managed-parent"}`))
		if err := json.Unmarshal(encoded, &listing); err != nil {
			t.Fatal(err)
		}
		return listing
	}
	for {
		if current := read(); current.Phase == "ready" && len(current.Items) == 0 {
			break
		}
		select {
		case <-time.After(5 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("Steward did not recover")
		}
	}
	if outcome := companionResult(t, ctx, process, companionapi.CreateMethod, json.RawMessage(`{"name":"reviewer"}`)); outcome.OK || outcome.Error == nil || outcome.Error.Code != connection.ErrorBadRequest {
		t.Fatalf("missing parent accepted: %+v", outcome)
	}
	if outcome := companionResult(t, ctx, process, companionapi.CreateMethod, json.RawMessage(`{"parentSessionId":"managed-parent","name":"missing"}`)); outcome.OK || outcome.Error == nil || outcome.Error.Code != connection.ErrorCompanionNotFound {
		t.Fatalf("absent policy accepted: %+v", outcome)
	}
	var created companionapi.View
	if err := json.Unmarshal(callHTTP(t, ctx, process, companionapi.CreateMethod, json.RawMessage(`{"parentSessionId":"managed-parent","name":"reviewer","config":{"enabled":true,"configuration":"review-config"}}`)), &created); err != nil {
		t.Fatal(err)
	}
	if !created.Persisted || created.Revision != 1 || created.ChildSessionID == "" {
		t.Fatalf("creation intent was not persisted: %+v", created)
	}
	current := awaitCompanionHTTP(t, ctx, read, "reviewer", "ready")
	if current.CreationID == "" || current.ChildSessionID != created.ChildSessionID {
		t.Fatalf("child identity changed during startup: %+v", current)
	}
	if outcome := companionResult(t, ctx, process, companionapi.ConfigureMethod, json.RawMessage(`{"parentSessionId":"managed-parent","name":"reviewer","expectedRevision":2,"enabled":false}`)); outcome.OK || outcome.Error == nil || outcome.Error.Code != connection.ErrorCompanionConflict {
		t.Fatalf("stale revision accepted: %+v", outcome)
	}
	var restarted companionapi.View
	request, err := json.Marshal(companionapi.RestartRequest{
		ParentSessionID:    "managed-parent",
		Name:               "reviewer",
		ExpectedRevision:   current.Revision,
		ExpectedCreationID: current.CreationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(callHTTP(t, ctx, process, companionapi.RestartMethod, request), &restarted); err != nil {
		t.Fatal(err)
	}
	current = awaitCompanionHTTP(t, ctx, read, "reviewer", "ready")
	if current.CreationID == restarted.CreationID || current.ChildSessionID != created.ChildSessionID {
		t.Fatalf("restart did not replace only the child lifetime: %+v %+v", restarted, current)
	}
	var disabled companionapi.View
	if err := json.Unmarshal(callHTTP(t, ctx, process, companionapi.ConfigureMethod, json.RawMessage(`{"parentSessionId":"managed-parent","name":"reviewer","expectedRevision":1,"enabled":false}`)), &disabled); err != nil {
		t.Fatal(err)
	}
	if !disabled.Persisted || disabled.Revision != 2 || disabled.Enabled {
		t.Fatalf("disable target was not recorded: %+v", disabled)
	}
	awaitCompanionHTTP(t, ctx, read, "reviewer", "stopped")
	var removed companionapi.View
	if err := json.Unmarshal(callHTTP(t, ctx, process, companionapi.RemoveMethod, json.RawMessage(`{"parentSessionId":"managed-parent","name":"reviewer","expectedRevision":2}`)), &removed); err != nil {
		t.Fatal(err)
	}
	if removed.Revision != 3 || !removed.Removed || removed.Enabled {
		t.Fatalf("tombstone was not recorded: %+v", removed)
	}
	if err := process.Close(ctx); err != nil {
		t.Fatal(err)
	}
	process, err = New(configuration, providerEnvironment{
		home: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Start(ctx); err != nil {
		t.Fatal(err)
	}
	callHTTP(t, ctx, process, "session.create", json.RawMessage(`{"sessionId":"managed-parent"}`))
	for {
		current := read()
		if current.Phase == "ready" && len(current.Items) == 1 && current.Items[0].Name == "reviewer" && current.Items[0].Removed && current.Items[0].ChildSessionID == created.ChildSessionID && current.Items[0].CreationID == "" {
			break
		}
		select {
		case <-time.After(5 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("removed Companion revived after restart: %+v", current)
		}
	}
}

func companionResult(t *testing.T, ctx context.Context, process *Server, method string, payload json.RawMessage) connection.RPCResult {
	t.Helper()
	body, err := json.Marshal(connection.ClientRequest{
		Type:    connection.ClientRequestType,
		RPCID:   connection.RPCID(method),
		Method:  method,
		Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+process.Address()+"/api/"+method, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var envelope connection.ServerResponse
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("%s status=%d error=%v", method, response.StatusCode, err)
	}
	return envelope.Result
}

func awaitCompanionHTTP(t *testing.T, ctx context.Context, read func() companionapi.Listing, name string, phase string) companionapi.View {
	t.Helper()
	for {
		current := read()
		for _, entry := range current.Items {
			if entry.Name == name && entry.Phase == phase {
				return entry
			}
		}
		select {
		case <-time.After(5 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("Companion %s did not reach %s: %+v", name, phase, current)
		}
	}
}

func TestCompanionPreparationFailureRestartsThroughHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	root := t.TempDir()
	process, err := New(Config{
		Address:           "127.0.0.1:0",
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspaces.sqlite"),
		CredentialsPath:   filepath.Join(root, "credentials.json"),
		Agents: map[string]settings.Values{
			"review-config": {
				Tools: map[string]tools.Config{"send_message": {}},
			},
		},
		Companions: map[string]companions.Config{
			"reviewer": {
				Enabled:       true,
				Configuration: "review-config",
				Inherit: companions.Inheritance{
					Tools: companions.InheritAllTools,
				},
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
	callHTTP(t, ctx, process, "session.create", json.RawMessage(`{"sessionId":"retry-parent"}`))
	parent := serverReady(t, ctx, process, "retry-parent")
	read := func() companionapi.Listing {
		t.Helper()
		var listing companionapi.Listing
		if err := json.Unmarshal(callHTTP(t, ctx, process, companionapi.ListMethod, json.RawMessage(`{"parentSessionId":"retry-parent"}`)), &listing); err != nil {
			t.Fatal(err)
		}
		return listing
	}
	failed := awaitCompanionHTTP(t, ctx, read, "reviewer", "start_failed")
	if failed.Persisted || failed.Revision != 0 || failed.CreationID != "" || !failed.Failure {
		t.Fatalf("incorrect preparation failure: %+v", failed)
	}
	settingsHTTP(t, ctx, process, settingsapi.UpdateCompanionMethod, settingsapi.UpdateCompanionRequest{
		Name: "reviewer",
		Config: companions.Config{
			Enabled:       true,
			Configuration: "review-config",
		},
	})
	payload, err := json.Marshal(companionapi.RestartRequest{
		ParentSessionID:    "retry-parent",
		Name:               "reviewer",
		ExpectedRevision:   failed.Revision,
		ExpectedCreationID: failed.CreationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	callHTTP(t, ctx, process, companionapi.RestartMethod, payload)
	recovered := awaitCompanionHTTP(t, ctx, read, "reviewer", "ready")
	if !recovered.Persisted || recovered.ChildSessionID == "" || recovered.CreationID == "" {
		t.Fatalf("retry did not create a durable child: %+v", recovered)
	}
	if current := serverReady(t, ctx, process, "retry-parent"); current.Creation != parent.Creation {
		t.Fatal("child preparation retry replaced parent")
	}
}
