package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gorenx/goren/agent"
	companionapi "github.com/gorenx/goren/apiproxy/companion"
	sessionevents "github.com/gorenx/goren/apiproxy/session/events"
	settingsapi "github.com/gorenx/goren/apiproxy/settings"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/settings"
	"github.com/gorenx/goren/subagent/companion"
	"github.com/gorenx/goren/subagent/tools/report"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	"github.com/gorenx/goren/tools/tool"
)

func TestCompanionConfigurationRestartsOnlyCompanion(t *testing.T) {
	type request struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	requests := make(chan request, 4)
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		var input request
		if err := json.NewDecoder(incoming.Body).Decode(&input); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- input
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer remote.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	root := t.TempDir()
	original := settings.Values{
		Model: agent.Options{
			Model: "deepseek-chat",
		},
		Prompts: map[string]settings.PromptConfig{
			"default": {
				Persona: new("companion-persona-original"),
			},
		},
	}
	process, err := New(Config{
		Address:           "127.0.0.1:0",
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspaces.sqlite"),
		CredentialsPath:   filepath.Join(root, "credentials.json"),
		Provider: deepseek.Config{
			BaseURL: &remote.URL,
		},
		Agents: map[string]settings.Values{
			"review-config": original,
		},
		Companions: map[string]companion.Config{
			"reviewer": {
				Enabled:       true,
				Configuration: "review-config",
			},
		},
		Tools: toolregistry.Catalog{
			Schemas: []llm.ToolSchema{echoSchema("echo settings")},
		},
		Providers: tool.Providers{"echo": &echoProvider{}},
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
	callHTTP(t, ctx, process, "session.create", json.RawMessage(`{"sessionId":"configuration-parent"}`))
	read := func() companionapi.Listing {
		t.Helper()
		var listing companionapi.Listing
		if err := json.Unmarshal(callHTTP(t, ctx, process, companionapi.ListMethod, json.RawMessage(`{"parentSessionId":"configuration-parent"}`)), &listing); err != nil {
			t.Fatal(err)
		}
		return listing
	}
	parent := serverReady(t, ctx, process, "configuration-parent")
	child := awaitCompanionHTTP(t, ctx, read, "reviewer", "ready")
	// Creation copies the template into an independent named configuration.
	var directory settingsapi.DescribeValue
	if err := json.Unmarshal(callHTTP(t, ctx, process, settingsapi.DescribeMethod, json.RawMessage(`{}`)), &directory); err != nil {
		t.Fatal(err)
	}
	var configurationName string
	var configurationRevision int64
	for _, entry := range directory.Namespaces {
		if entry.NS == "agent" && entry.Name != "review-config" {
			if configurationName != "" {
				t.Fatal("unexpected additional Agent configuration")
			}
			configurationName = entry.Name
			configurationRevision = entry.Revision
		}
	}
	if configurationName == "" {
		t.Fatal("Companion independent configuration missing")
	}
	feed, _, err := websocket.Dial(ctx, "ws://"+process.Address()+connection.MuxEventsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer feed.CloseNow()
	if readConversationFrame(t, ctx, feed).Method != "session/subscribed" {
		t.Fatal("missing subscription baseline")
	}
	for index, marker := range []string{"companion-persona-original", "companion-persona-updated", "companion-persona-original"} {
		expectedModel := "deepseek-chat"
		if index == 1 {
			updated := original.Clone()
			updated.Model.Model = "deepseek-reasoner"
			expectedModel = updated.Model.Model
			updated.Prompts["default"] = settings.PromptConfig{
				Persona: &marker,
			}
			updated.Tools = map[string]tools.Config{"echo": {}}
			saved := settingsHTTP(t, ctx, process, settingsapi.UpdateAgentMethod, settingsapi.UpdateAgentRequest{
				Name:             configurationName,
				ExpectedRevision: configurationRevision,
				Config:           updated,
			})
			configurationRevision = saved.Revision
		}
		if index == 2 {
			settingsHTTP(t, ctx, process, settingsapi.UpdateAgentMethod, settingsapi.UpdateAgentRequest{
				Name:             configurationName,
				ExpectedRevision: configurationRevision,
				Config:           original,
			})
		}
		if index > 0 {
			previous := child.CreationID
			for {
				current := companionapi.Listing{}
				response := companionResult(t, ctx, process, companionapi.ListMethod, json.RawMessage(`{"parentSessionId":"configuration-parent"}`))
				if response.OK {
					if err := json.Unmarshal(response.Value, &current); err != nil {
						t.Fatal(err)
					}
				}
				for _, entry := range current.Items {
					if entry.Name == "reviewer" && entry.Phase == "ready" && entry.CreationID != previous {
						child = entry
						break
					}
				}
				if child.CreationID != previous {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("Companion was not restarted")
				case <-time.After(10 * time.Millisecond):
				}
			}
		}
		callHTTP(t, ctx, process, "session.prompt", json.RawMessage(fmt.Sprintf(`{"sessionId":%q,"mode":"queue","content":[{"type":"text","text":"review"}]}`, child.ChildSessionID)))
		select {
		case input := <-requests:
			if input.Model != expectedModel {
				t.Fatalf("Turn %d model = %q, want %q", index, input.Model, expectedModel)
			}
			found := false
			for _, message := range input.Messages {
				if message.Role == "system" && strings.Contains(string(message.Content), marker) {
					found = true
				}
			}
			if !found {
				t.Fatalf("Turn %d missing selected persona: %+v", index, input.Messages)
			}
			names := make(map[string]bool)
			for _, entry := range input.Tools {
				names[entry.Function.Name] = true
			}
			expectedTools := 1
			if index == 1 {
				expectedTools++
			}
			if !names[report.Schema().Name] || names["echo"] != (index == 1) || len(names) != expectedTools {
				t.Fatalf("Turn %d incorrect independent tools: %v", index, names)
			}
		case <-ctx.Done():
			t.Fatal("Companion model request missing")
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
			if string(notice.SessionID) == string(child.ChildSessionID) && notice.Event.Type == session.TurnEndEventName {
				break
			}
		}
	}
	current := awaitCompanionHTTP(t, ctx, read, "reviewer", "ready")
	if after := serverReady(t, ctx, process, "configuration-parent"); after.Creation != parent.Creation {
		t.Fatal("Companion configuration restarted parent")
	}
	if current.ChildSessionID != child.ChildSessionID {
		t.Fatal("restart replaced the persisted Companion Session")
	}
	// A change to the parent's own configuration does replace its installation,
	// including companions, while preserving both persisted Session identities.
	settingsHTTP(t, ctx, process, settingsapi.UpdatePromptMethod, settingsapi.UpdatePromptRequest{
		Name:   "default",
		Config: settings.PromptConfig{Persona: new("parent-replacement")},
	})
	awaitSettingsRestart(t, ctx, process, "configuration-parent", parent.Creation.CreationID)
	restored := awaitCompanionHTTP(t, ctx, read, "reviewer", "ready")
	if restored.ChildSessionID != child.ChildSessionID || restored.CreationID == child.CreationID {
		t.Fatal("parent restart did not restore Companion under a new lifetime")
	}

}
