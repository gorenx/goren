package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	settingsapi "github.com/gorenx/goren/apiproxy/settings"
	"github.com/gorenx/goren/subagent/companion"
	"github.com/gorenx/goren/systemprompt"
)

func TestCompanionSettingsHTTPPersistsAcrossServerRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	configuration := Config{
		Address:           "127.0.0.1:0",
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspaces.sqlite"),
		CredentialsPath:   filepath.Join(root, "credentials.json"),
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
	saved := settingsHTTP(t, ctx, process, settingsapi.UpdateCompanionMethod, settingsapi.UpdateCompanionRequest{
		Name: "reviewer",
		Config: companion.Config{
			Enabled: true,
			Prompt: systemprompt.Config{
				Persona: "review independently",
			},
		},
	})
	if saved.NS != "companion" || saved.Applies != settingsapi.ApplyNewInstance || saved.Revision != 1 || saved.AppliedRevision == nil || *saved.AppliedRevision != 1 {
		t.Fatalf("incorrect adoption scope: %+v", saved)
	}
	if err := process.Close(ctx); err != nil {
		t.Fatal(err)
	}
	process = start()
	var description settingsapi.DescribeValue
	if err := json.Unmarshal(callHTTP(t, ctx, process, settingsapi.DescribeMethod, json.RawMessage(`{}`)), &description); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range description.Namespaces {
		if entry.NS != "companion" || entry.Name != "reviewer" {
			continue
		}
		found = true
		var restored companion.Config
		if err := json.Unmarshal(entry.Value, &restored); err != nil {
			t.Fatal(err)
		}
		if !restored.Enabled || restored.Prompt.Persona != "review independently" || entry.Revision != saved.Revision {
			t.Fatal("restart lost persisted Companion configuration")
		}
	}
	if !found {
		t.Fatal("Companion missing from actual HTTP Settings catalog")
	}
	reset := settingsHTTP(t, ctx, process, settingsapi.ResetCompanionMethod, settingsapi.ResetCompanionRequest{
		Name:             "reviewer",
		ExpectedRevision: saved.Revision,
	})
	if reset.Revision != 2 || string(reset.Value) != "null" {
		t.Fatal("reset did not preserve cleared version")
	}
	if err := process.Close(ctx); err != nil {
		t.Fatal(err)
	}
	process = start()
	if err := json.Unmarshal(callHTTP(t, ctx, process, settingsapi.DescribeMethod, json.RawMessage(`{}`)), &description); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, entry := range description.Namespaces {
		if entry.NS == "companion" && entry.Name == "reviewer" {
			found = true
			if entry.Revision != 2 || string(entry.Value) != "null" {
				t.Fatal("restart lost reset tombstone")
			}
		}
	}
	if !found {
		t.Fatal("reset group revision missing after restart")
	}
}
