package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	agentregistry "github.com/gorenx/goren/agent/registry"
	commandsapi "github.com/gorenx/goren/apiproxy/commands"
	companionapi "github.com/gorenx/goren/apiproxy/companion"
	"github.com/gorenx/goren/connection"
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
	t.Cleanup(func() {
		if err := process.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
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
	process, err := New(Config{
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
	}, providerEnvironment{
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
	var listing companionapi.Listing
	read := func() companionapi.Listing {
		t.Helper()
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
