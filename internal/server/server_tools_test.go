package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gorenx/goren/agentmessage"
	sessionevents "github.com/gorenx/goren/apiproxy/session/events"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// HTTP, AgentLoop, configuration PubSub, Prompt, and the common ToolActor all run
// through production wiring. The test model and tool body expose external waits.
func TestHTTPToolsChangeOnlyBetweenTurns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	source := &echoProvider{
		entered: make(chan struct{}, 2),
		release: make(chan struct{}, 2),
	}
	requests := make(chan string, 4)
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"function"`
			} `json:"tools"`
		}
		err := json.NewDecoder(incoming.Body).Decode(&body)
		if err != nil {
			t.Errorf("model tool definitions: %+v, %v", body, err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, entry := range body.Tools {
			if entry.Function.Name == "echo" {
				requests <- entry.Function.Description
			}
			if entry.Function.Name == "approval" {
				t.Error("internal approval tool leaked to the model")
			}
		}
		number := calls.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		if number%2 == 1 {
			fmt.Fprintf(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"echo-%d\",\"type\":\"function\",\"function\":{\"name\":\"echo\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n", number)
		} else {
			fmt.Fprint(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		}
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer remote.Close()
	root := t.TempDir()
	process, err := New(Config{
		Address:           "127.0.0.1:0",
		WorkingDirectory:  root,
		SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
		WorkspaceDatabase: filepath.Join(root, "workspace.sqlite"),
		CredentialsPath:   filepath.Join(root, "credentials.json"),
		Provider:          deepseek.Config{BaseURL: &remote.URL},
		Tools:             toolregistry.Catalog{Schemas: []llm.ToolSchema{echoSchema("original")}},
		Providers:         tool.Providers{"echo": source},
	}, providerEnvironment{home: root})
	if err != nil {
		t.Fatal(err)
	}
	err = process.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		failure := process.Close(context.Background())
		if failure != nil {
			t.Error(failure)
		}
	})
	callHTTP(t, ctx, process, "credentials.set", json.RawMessage(`{"ref":"DEEPSEEK_API_KEY","value":"test-key"}`))
	callHTTP(t, ctx, process, "session.create", json.RawMessage(`{"sessionId":"tools"}`))
	feed, _, err := websocket.Dial(ctx, "ws://"+process.Address()+connection.MuxEventsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer feed.CloseNow()
	baseline := readConversationFrame(t, ctx, feed)
	if baseline.Method != "session/subscribed" {
		t.Fatalf("baseline: %+v", baseline)
	}
	directory, err := goakt.GrainOf[*catalogactor.RegistryGrain](ctx, process.system, catalogactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	for turn, description := range []string{"original", "updated"} {
		callHTTP(t, ctx, process, "session.prompt", json.RawMessage(`{"sessionId":"tools","mode":"queue","content":[{"type":"text","text":"use echo"}]}`))
		select {
		case <-source.entered:
		case <-ctx.Done():
			t.Fatal("tool body did not start")
		}
		if turn == 0 {
			result := runtimeRequest[catalogactor.Completion](t, ctx, process.system, directory, catalogactor.RegisterSource{
				Name:    "local",
				Catalog: toolregistry.Catalog{Schemas: []llm.ToolSchema{echoSchema("updated")}},
			})
			if result.Err != nil {
				t.Fatal(result.Err)
			}
		}
		if source.opens.Load() != int32(turn+1) || source.closes.Load() != int32(turn) {
			t.Fatalf("work replaced during Turn: opens=%d closes=%d", source.opens.Load(), source.closes.Load())
		}
		source.release <- struct{}{}
		results := 0
		for {
			frame := readConversationFrame(t, ctx, feed)
			if frame.Method != "session/event" {
				continue
			}
			var notification sessionevents.EventFrame
			err = json.Unmarshal(frame.Payload, &notification)
			if err != nil {
				t.Fatal(err)
			}
			if notification.Event.Type == session.ToolResultEventName {
				results++
			}
			if notification.Event.Type == session.TurnEndEventName {
				break
			}
		}
		if results != 1 {
			t.Fatalf("tool results=%d", results)
		}
		for step := 0; step < 2; step++ {
			select {
			case actual := <-requests:
				if actual != description {
					t.Fatalf("Turn %d Step %d schema=%q want=%q", turn, step, actual, description)
				}
			case <-ctx.Done():
				t.Fatal("missing model input")
			}
		}
	}
	err = process.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if source.opens.Load() != 2 || source.closes.Load() != 2 {
		t.Fatalf("unreleased work: %d/%d", source.opens.Load(), source.closes.Load())
	}
}

func echoSchema(description string) llm.ToolSchema {
	return llm.ToolSchema{
		Name:        "echo",
		Description: description,
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}
}

type echoProvider struct {
	opens   atomic.Int32
	closes  atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (source *echoProvider) Open(context.Context) (tool.Implementation, error) {
	source.opens.Add(1)
	body := &echoWork{
		closed:  &source.closes,
		entered: source.entered,
		release: source.release,
	}
	return tool.Implementation{
		Definition: tools.ToolDefinition{
			Name:       "echo",
			Parameters: json.RawMessage(`{"type":"object"}`),
			Output: tools.ToolOutputDefinition{
				Schema:   json.RawMessage(`{"type":"object"}`),
				Renderer: body,
			},
			Executor: body,
		},
		Resource: body,
	}, nil
}

type echoWork struct {
	closed  *atomic.Int32
	entered chan<- struct{}
	release <-chan struct{}
}

func (body *echoWork) Execute(_ json.RawMessage, scope tools.ToolRunContext) (json.RawMessage, error) {
	body.entered <- struct{}{}
	select {
	case <-body.release:
		return json.RawMessage(`{}`), nil
	case <-scope.Context.Done():
		return nil, context.Cause(scope.Context)
	}
}
func (*echoWork) Render(json.RawMessage, json.RawMessage) ([]agentmessage.ContentBlock, error) {
	return []agentmessage.ContentBlock{agentmessage.NewTextBlock("echo completed")}, nil
}
func (body *echoWork) Close(context.Context) error { body.closed.Add(1); return nil }
