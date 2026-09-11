package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session/persistence"
)

func TestServerHTTPPersistenceAndShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	settings := Config{
		Address:           "127.0.0.1:0",
		Version:           "integration",
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspaces.sqlite"),
		CredentialsPath:   filepath.Join(root, ".credentials.json"),
	}
	start := func() *Server {
		t.Helper()
		process, err := New(settings, providerEnvironment{
			home: root,
		})
		if err != nil {
			t.Fatal(err)
		}
		err = process.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			failure := process.Close(cleanup)
			if failure != nil {
				t.Error(failure)
			}
		})
		return process
	}
	process := start()
	call := func(method string, payload json.RawMessage) json.RawMessage {
		t.Helper()
		body, err := json.Marshal(connection.ClientRequest{
			Type:    "client-request",
			RPCID:   "integration",
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
		err = json.NewDecoder(response.Body).Decode(&envelope)
		if err != nil || response.StatusCode != http.StatusOK || !envelope.Result.OK {
			t.Fatalf("%s: status=%d reply=%+v error=%v", method, response.StatusCode, envelope, err)
		}
		return envelope.Result.Value
	}
	call("host.describe", []byte(`{}`))
	call("session.create", []byte(`{"sessionId":"integration"}`))
	call("session.rename", []byte(`{"sessionId":"integration","title":"Persisted title"}`))
	feed, _, err := websocket.Dial(ctx, "ws://"+process.Address()+connection.HostEventsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer feed.CloseNow()
	encodedPath, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	call("workspace.create", []byte(`{"path":`+string(encodedPath)+`}`))
	_, frame, err := feed.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(frame, []byte("host/workspace-changed")) {
		t.Fatalf("workspace PubSub did not reach WebSocket: %s", frame)
	}
	call("session.prompt", []byte(`{"sessionId":"integration","mode":"queue","content":[{"type":"text","text":"retained shutdown input"}]}`))
	oldAddress := process.Address()
	err = process.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := net.DialTimeout("tcp", oldAddress, 100*time.Millisecond)
	if err == nil {
		probe.Close()
		t.Fatal("listener survived Close")
	}
	process = start()
	listing := call("session.list", []byte(`{}`))
	if !bytes.Contains(listing, []byte("Persisted title")) {
		t.Fatalf("Session title missing after restart: %s", listing)
	}
	durable, err := persistence.NewReader(process.sessions)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := durable.Inspect(ctx, "integration")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte("retained shutdown input")) {
		t.Fatal("accepted input was not persisted on shutdown")
	}
}

func TestServerListenerFailureRollsBackAndAllowsNewInstance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	root := t.TempDir()
	settings := Config{
		Address:           occupied.Addr().String(),
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspaces.sqlite"),
		CredentialsPath:   filepath.Join(root, ".credentials.json"),
	}
	process, err := New(settings, providerEnvironment{home: root})
	if err != nil {
		t.Fatal(err)
	}
	err = process.Start(ctx)
	if err == nil {
		t.Fatal("occupied listener accepted")
	}
	if process.system != nil || process.sessions != nil || process.index != nil || process.workspace != nil {
		t.Fatal("failed startup retained acquired resources after rollback")
	}
	err = process.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	occupied.Close()
	replacement, err := New(settings, providerEnvironment{home: root})
	if err != nil {
		t.Fatal(err)
	}
	err = replacement.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = replacement.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
}
