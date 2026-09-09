package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gorenx/goren/approval"
	approvalactor "github.com/gorenx/goren/approval/actor"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/session"
	sessionsqlite "github.com/gorenx/goren/session/persistence/sqlite"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"github.com/gorenx/goren/toolaskuser"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type permissionPolicy struct{}

func (permissionPolicy) EvaluateCall(context.Context, tools.PreExecuteRequest) (tools.PreExecuteOutcome, error) {
	return tools.PreExecuteOutcome{
		Decision: tools.AskDecision{
			Reason: "Confirm this operation",
		},
	}, nil
}

// The real server, AgentLoop, Registry, Runtime, common ToolActor, Session and
// Interaction Gateway execute normally. Only the external model/tool effects are
// controlled, so a test cannot bypass missing startup or reply wiring.
func TestHTTPBuiltInToolInteractions(t *testing.T) {
	for _, scenario := range []string{"question-answer", "question-cancel", "approval-allow", "approval-reject", "approval-cancel", "approval-close", "question-server-close", "approval-server-close"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			question := strings.HasPrefix(scenario, "question")
			name := "echo"
			arguments := `{}`
			requestedMethod := "approval/requested"
			if question {
				name = toolaskuser.Name
				arguments = `{"questions":[{"id":"q","question":"Continue?"}]}`
				requestedMethod = "question/requested"
			}
			var modelCalls atomic.Int32
			remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var body struct {
					Tools []struct {
						Function llm.ToolSchema `json:"function"`
					} `json:"tools"`
					Messages json.RawMessage `json:"messages"`
				}
				err := json.NewDecoder(request.Body).Decode(&body)
				if err != nil {
					t.Error(err)
					return
				}
				found := false
				for _, entry := range body.Tools {
					if entry.Function.Name == name {
						found = true
					}
					if entry.Function.Name == approval.ServiceName {
						t.Error("internal approval tool leaked to model")
					}
				}
				if !found {
					t.Errorf("model did not receive schema for %s", name)
				}
				writer.Header().Set("Content-Type", "text/event-stream")
				if modelCalls.Add(1) == 1 {
					fmt.Fprintf(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"operation\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%q}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n", name, arguments)
				} else {
					if !bytes.Contains(body.Messages, []byte(`"tool_call_id":"operation"`)) {
						t.Errorf("model continuation missed tool result: %s", body.Messages)
					}
					fmt.Fprint(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
				}
				fmt.Fprint(writer, "data: [DONE]\n\n")
			}))
			defer remote.Close()
			source := &echoProvider{
				entered: make(chan struct{}, 2),
				release: make(chan struct{}, 2),
			}
			source.release <- struct{}{}
			root := t.TempDir()
			settings := Config{
				Address:           "127.0.0.1:0",
				WorkingDirectory:  root,
				SessionDatabase:   filepath.Join(root, "sessions.sqlite"),
				WorkspaceDatabase: filepath.Join(root, "workspace.sqlite"),
				CredentialsPath:   filepath.Join(root, "credentials.json"),
				Provider: deepseek.Config{
					BaseURL: &remote.URL,
				},
			}
			if !question {
				settings.Tools = toolregistry.Catalog{
					Schemas: []llm.ToolSchema{echoSchema("Needs permission")},
					Policies: map[string]toolregistry.Policy{
						"echo": {
							Before: permissionPolicy{},
						},
					},
				}
				settings.Providers = tool.Providers{"echo": source}
			}
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
				cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				failure := process.Close(cleanup)
				if failure != nil {
					t.Error(failure)
				}
			})
			callHTTP(t, ctx, process, "credentials.set", json.RawMessage(`{"ref":"DEEPSEEK_API_KEY","value":"test-key"}`))
			callHTTP(t, ctx, process, "session.create", json.RawMessage(`{"sessionId":"interaction"}`))
			feed, _, err := websocket.Dial(ctx, "ws://"+process.Address()+connection.MuxEventsPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer feed.CloseNow()
			callHTTP(t, ctx, process, "session.prompt", json.RawMessage(`{"sessionId":"interaction","mode":"queue","content":[{"type":"text","text":"Use tool"}]}`))
			var pending connection.ServerRequest
			for pending.Method != requestedMethod {
				pending = readConversationFrame(t, ctx, feed)
				if pending.Method == "session/event" && bytes.Contains(pending.Payload, []byte(`"type":"turn/end"`)) {
					t.Fatalf("turn ended before interaction: %s", pending.Payload)
				}
			}
			var approvalID approval.RequestID
			if !question {
				history := interactionHistory(t, ctx, process)
				if history.Events[len(history.Events)-1].Type != approval.AskedEventName {
					t.Fatal("client was asked before approval audit commitment")
				}
				var asked approval.Asked
				err = json.Unmarshal(history.Events[len(history.Events)-1].Data, &asked)
				if err != nil || asked.ToolName != "echo" || asked.CallID == nil || *asked.CallID != "operation" {
					t.Fatalf("incorrect audit: %+v %v", asked, err)
				}
				approvalID = asked.ID
			}
			// Reconnect while waiting: exactly the same request remains answerable.
			feed.CloseNow()
			feed, _, err = websocket.Dial(ctx, "ws://"+process.Address()+connection.MuxEventsPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer feed.CloseNow()
			for {
				replayed := readConversationFrame(t, ctx, feed)
				if replayed.Method == requestedMethod {
					if replayed.RPCID != pending.RPCID || !bytes.Equal(replayed.Payload, pending.Payload) {
						t.Fatal("reconnect replaced pending interaction")
					}
					break
				}
			}
			serverClosed := strings.HasSuffix(scenario, "server-close")
			if serverClosed {
				err = process.Close(ctx)
				if err != nil {
					t.Fatal(err)
				}
			} else if scenario == "approval-close" {
				err = closeAgents(ctx, process.system, process.agents)
				if err != nil {
					t.Fatal(err)
				}
			} else if strings.HasSuffix(scenario, "cancel") {
				callHTTP(t, ctx, process, "session.cancel", json.RawMessage(`{"sessionId":"interaction"}`))
			} else {
				value := json.RawMessage(`{"sessionId":"interaction","answer":{"answers":[{"id":"q","selected":[],"custom":"Yes"}]}}`)
				if !question {
					outcome := approval.OutcomeAllowedOnce
					if scenario == "approval-reject" {
						outcome = approval.OutcomeRejected
					}
					value, err = json.Marshal(map[string]string{
						"sessionId":  "interaction",
						"approvalId": string(approvalID),
						"outcome":    string(outcome),
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				receipt := answerHTTP(t, ctx, process, pending.RPCID, value)
				if !receipt.Accepted {
					t.Fatalf("client answer rejected: %+v", receipt)
				}
				if answerHTTP(t, ctx, process, pending.RPCID, value).Accepted {
					t.Fatal("duplicate answer accepted")
				}
			}
			if !serverClosed {
				for {
					frame := readConversationFrame(t, ctx, feed)
					if frame.Method == "session/event" && bytes.Contains(frame.Payload, []byte(`"type":"turn/end"`)) {
						break
					}
				}
				err = closeAgents(ctx, process.system, process.agents)
				if err != nil {
					t.Fatal(err)
				}
			}
			storage := process.sessions
			if serverClosed {
				storage, err = sessionsqlite.Open(ctx, sessionsqlite.Config{
					Path:        settings.SessionDatabase,
					JournalMode: sessionsqlite.JournalWAL,
				})
				if err != nil {
					t.Fatal(err)
				}
				defer storage.Close(context.Background())
			}
			stored, err := storage.Load(ctx, "interaction")
			if err != nil || stored == nil {
				t.Fatalf("persisted Session: %+v %v", stored, err)
			}
			askedCount, decidedCount, results := 0, 0, 0
			var decidedSeq int64 = -1
			for _, event := range stored.Events {
				switch event.Type {
				case approval.AskedEventName:
					askedCount++
				case approval.DecidedEventName:
					decidedCount++
					decidedSeq = event.Seq
					var decision approval.Decided
					err = json.Unmarshal(event.Data, &decision)
					want := approval.OutcomeCancelled
					if scenario == "approval-allow" {
						want = approval.OutcomeAllowedOnce
					} else if scenario == "approval-reject" {
						want = approval.OutcomeRejected
					}
					if err != nil || decision.ID != approvalID || decision.Outcome != want {
						t.Fatalf("decision: %+v %v, want %s", decision, err, want)
					}
				case session.ToolResultEventName:
					results++
					if !question && (decidedSeq < 0 || decidedSeq >= event.Seq) {
						t.Fatal("tool result preceded decided audit")
					}
					if scenario == "question-answer" && !bytes.Contains(event.Data, []byte("Yes")) {
						t.Fatalf("tool result lost client answer: %s", event.Data)
					}
				}
			}
			if results != 1 || (!question && (askedCount != 1 || decidedCount != 1)) {
				t.Fatalf("audit/result counts: %d/%d/%d", askedCount, decidedCount, results)
			}
			if (scenario == "approval-allow") != (len(source.entered) == 1) {
				t.Fatalf("original tool body admission wrong: %d calls", len(source.entered))
			}
		})
	}
}

func answerHTTP(t *testing.T, ctx context.Context, process *Server, id connection.RPCID, value json.RawMessage) connection.RPCReceipt {
	t.Helper()
	body, err := json.Marshal(connection.ClientResponse{
		Type:  connection.ClientResponseType,
		RPCID: id,
		Result: connection.RPCResult{
			OK:    true,
			Value: value,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+process.Address()+connection.RespondPath, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var receipt connection.RPCReceipt
	err = json.NewDecoder(response.Body).Decode(&receipt)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("respond: %d %+v %v", response.StatusCode, receipt, err)
	}
	return receipt
}

func interactionHistory(t *testing.T, ctx context.Context, process *Server) session.Snapshot {
	t.Helper()
	directory, err := goakt.GrainOf[*sessionregistry.RegistryGrain](ctx, process.system, sessionregistry.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	logs, err := approvalactor.NewSessions(process.system, directory, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := logs.Open(ctx, "interaction")
	if err != nil {
		t.Fatal(err)
	}
	history, err := selected.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return history
}
