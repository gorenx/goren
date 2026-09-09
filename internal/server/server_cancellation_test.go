package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gorenx/goren/llm/deepseek"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func TestHTTPStopPublishesExecutionStatus(t *testing.T) {
	testHTTPStop(t, false)
}

func TestHTTPStopAfterIdle(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the framework idle passivation deadline")
	}
	testHTTPStop(t, true)
}

func testHTTPStop(t *testing.T, idle bool) {
	t.Helper()
	started := make(chan struct{}, 8)
	cancelled := make(chan struct{}, 8)
	release := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n")
		writer.(http.Flusher).Flush()
		started <- struct{}{}
		select {
		case <-incoming.Context().Done():
			cancelled <- struct{}{}
		case <-release:
		}
	}))
	defer remote.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), goakt.DefaultPassivationTimeout+15*time.Second)
	defer cancel()
	root := t.TempDir()
	process, err := New(Config{
		Address:           "127.0.0.1:0",
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspace.sqlite"),
		CredentialsPath:   filepath.Join(root, "credentials.json"),
		Provider:          deepseek.Config{BaseURL: &remote.URL},
	}, providerEnvironment{home: root})
	if err != nil {
		t.Fatal(err)
	}
	err = process.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close(context.Background())
	callHTTP(t, ctx, process, "credentials.set", json.RawMessage(`{"ref":"DEEPSEEK_API_KEY","value":"test-key"}`))
	host, _, err := websocket.Dial(ctx, "ws://"+process.Address()+"/api/events.host", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.CloseNow()
	if idle {
		timer := time.NewTimer(goakt.DefaultPassivationTimeout + time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	callHTTP(t, ctx, process, "session.create", json.RawMessage(`{"sessionId":"stop"}`))
	feed, _, err := websocket.Dial(ctx, "ws://"+process.Address()+"/api/events.mux", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer feed.CloseNow()
	callHTTP(t, ctx, process, "session.prompt", json.RawMessage(`{"sessionId":"stop","mode":"queue","content":[{"type":"text","text":"hello"}]}`))
	waitStatus := func(running bool) {
		for {
			frame := readConversationFrame(t, ctx, host)
			var status struct {
				Type      string `json:"type"`
				SessionID string `json:"sessionId"`
				Running   bool   `json:"running"`
			}
			failure := json.Unmarshal(frame.Payload, &status)
			if failure != nil {
				t.Fatal(failure)
			}
			if status.Type == "host/session-status" && status.SessionID == "stop" {
				if status.Running != running {
					t.Fatalf("running = %v, want %v", status.Running, running)
				}
				return
			}
		}
	}
	waitStatus(true)
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("model request never started")
	}
	callHTTP(t, ctx, process, "session.cancel", json.RawMessage(`{"sessionId":"stop"}`))
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("model I/O was not cancelled")
	}
	waitStatus(false)
	for {
		frame := readConversationFrame(t, ctx, feed)
		var fact struct {
			Type  string `json:"type"`
			Event struct {
				Type string `json:"type"`
			} `json:"event"`
		}
		err = json.Unmarshal(frame.Payload, &fact)
		if err != nil {
			t.Fatal(err)
		}
		if fact.Type == "session/event" && fact.Event.Type == "turn/end" {
			break
		}
	}
}
