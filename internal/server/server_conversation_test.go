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

	"github.com/coder/websocket"
	"github.com/gorenx/goren/agent"
	sessionevents "github.com/gorenx/goren/apiproxy/session/events"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/session/persistence/sqlite"
)

func TestHTTPPromptDrivesModelAndPersistsCompletedTurn(t *testing.T) {
	requested := make(chan struct{}, 4)
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		if incoming.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing test credential")
		}
		requested <- struct{}{}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"driver response\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
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
		Provider:          deepseek.Config{BaseURL: &remote.URL},
	}, providerEnvironment{home: root})
	if err != nil {
		t.Fatal(err)
	}
	err = process.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		failure := process.Close(cleanup)
		if failure != nil {
			t.Error(failure)
		}
	})
	callHTTP(t, ctx, process, "credentials.set", json.RawMessage(`{"ref":"DEEPSEEK_API_KEY","value":"test-key"}`))
	callHTTP(t, ctx, process, "session.create", json.RawMessage(`{"sessionId":"driver"}`))
	feed, _, err := websocket.Dial(ctx, "ws://"+process.Address()+connection.MuxEventsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer feed.CloseNow()
	baseline := readConversationFrame(t, ctx, feed)
	if baseline.Method != "session/subscribed" {
		t.Fatalf("missing subscription baseline: %+v", baseline)
	}
	callHTTP(t, ctx, process, "session.prompt", json.RawMessage(`{"sessionId":"driver","mode":"queue","content":[{"type":"text","text":"hello"}]}`))
	select {
	case <-requested:
	case <-ctx.Done():
		t.Fatal("accepted input never reached the model")
	}
	counts := make(map[string]int)
	lastSequence := int64(-1)
	for counts[session.TurnEndEventName] == 0 {
		frame := readConversationFrame(t, ctx, feed)
		if frame.Method != "session/event" {
			continue
		}
		var notification sessionevents.EventFrame
		err = json.Unmarshal(frame.Payload, &notification)
		if err != nil {
			t.Fatal(err)
		}
		if notification.SessionID != "driver" || notification.Event.Seq <= lastSequence {
			t.Fatalf("incorrect Session event order: %+v after %d", notification, lastSequence)
		}
		lastSequence = notification.Event.Seq
		counts[notification.Event.Type]++
		if notification.Event.Type == session.AssistantMessageEventName && !bytes.Contains(frame.Payload, []byte("driver response")) {
			t.Fatalf("model reply missing from downlink: %s", frame.Payload)
		}
	}
	if counts[session.AssistantChunkEventName] == 0 || counts[session.AssistantMessageEventName] != 1 || counts[session.StepEndEventName] != 1 {
		t.Fatalf("incomplete WebSocket Turn: %v", counts)
	}
	durable, err := persistence.NewReader(process.sessions)
	if err != nil {
		t.Fatal(err)
	}
	for {
		stored, failure := durable.Inspect(ctx, "driver")
		if failure == nil {
			counts := map[string]int{}
			for _, event := range stored.Events {
				counts[event.Type]++
			}
			if counts[session.TurnEndEventName] == 1 {
				if counts[session.AssistantMessageEventName] != 1 || counts[session.AssistantChunkEventName] == 0 || counts[session.StepEndEventName] != 1 {
					t.Fatalf("incomplete persisted turn: %v", counts)
				}
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("model output did not settle durably: %v", failure)
		case <-time.After(10 * time.Millisecond):
		}
	}
	listing := callHTTP(t, ctx, process, "session.list", json.RawMessage(`{}`))
	if bytes.Contains(listing, []byte(`"running":true`)) {
		t.Fatalf("completed Turn left Agent busy: %s", listing)
	}
}

func readConversationFrame(t *testing.T, ctx context.Context, feed *websocket.Conn) connection.ServerRequest {
	t.Helper()
	_, payload, err := feed.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var envelope connection.ServerRequest
	err = json.Unmarshal(payload, &envelope)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func callHTTP(t *testing.T, ctx context.Context, process *Server, method string, payload json.RawMessage) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(connection.ClientRequest{
		Type:    "client-request",
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
	err = json.NewDecoder(response.Body).Decode(&envelope)
	if err != nil || response.StatusCode != http.StatusOK || !envelope.Result.OK {
		t.Fatalf("%s: HTTP %d, result=%+v, error=%v", method, response.StatusCode, envelope.Result.Error, err)
	}
	return envelope.Result.Value
}

// Shutdown must cancel a live provider before waiting for Turn settlement.
func TestHTTPCloseCancelsStreamingModelAndFlushesTurn(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n")
		writer.(http.Flusher).Flush()
		close(started)
		select {
		case <-incoming.Context().Done():
			close(cancelled)
		case <-release:
		}
	}))
	defer remote.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	database := filepath.Join(root, "sessions.sqlite")
	process, err := New(Config{
		Address:           "127.0.0.1:0",
		WorkingDirectory:  root,
		SessionDatabase:   database,
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
	callHTTP(t, ctx, process, "credentials.set", json.RawMessage(`{"ref":"DEEPSEEK_API_KEY","value":"test-key"}`))
	callHTTP(t, ctx, process, "session.create", json.RawMessage(`{"sessionId":"shutdown"}`))
	callHTTP(t, ctx, process, "session.prompt", json.RawMessage(`{"sessionId":"shutdown","mode":"queue","content":[{"type":"text","text":"hello"}]}`))
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("provider did not start")
	}
	callHTTP(t, ctx, process, "session.prompt", json.RawMessage(`{"sessionId":"shutdown","mode":"queue","content":[{"type":"text","text":"resume later"}]}`))
	cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	err = process.Close(cleanup)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-cleanup.Done():
		t.Fatal("provider was not cancelled")
	}
	backend, err := sqlite.Open(ctx, sqlite.Config{
		Path:        database,
		JournalMode: sqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close(context.Background())
	durable, err := persistence.NewReader(backend)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := durable.Inspect(ctx, "shutdown")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := agent.NewInbox(stored.Header, stored.Events)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.NextTurn()) != 1 {
		t.Fatal("shutdown lost or consumed queued input")
	}
	counts := make(map[string]int)
	for _, event := range stored.Events {
		counts[event.Type]++
	}
	if counts[session.StepEndEventName] != 1 || counts[session.TurnEndEventName] != 1 {
		t.Fatalf("shutdown did not persist completed execution: %v", counts)
	}
}
