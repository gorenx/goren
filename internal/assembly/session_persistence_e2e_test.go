package assembly

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/apiproxy"
	protocol "github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

func TestDefaultCompositionListsHistoryAndResumesAColdSQLiteSession(t *testing.T) {
	requestContext := context.Background()
	workspace := t.TempDir()
	dataDirectory := t.TempDir()
	databasePath := dataDirectory + "/sessions.sqlite"
	workspaceDatabasePath := dataDirectory + "/workspaces.sqlite"
	firstCatalog, err := NewCatalog(Environment{WorkingDirectory: workspace})
	if err != nil {
		t.Fatal(err)
	}
	firstSpecs, err := DefaultSpecs("127.0.0.1:0", "test", databasePath, workspaceDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	firstRuntime := plugin.NewRuntime()
	if _, err := Load(requestContext, firstRuntime, firstCatalog, firstSpecs); err != nil {
		t.Fatal(err)
	}
	firstProbe := probePlugin{body: func(_ context.Context, pluginScope *plugin.Scope) error {
		agentRegistry, found := plugin.Require(pluginScope, agentcore.Service)
		if !found {
			t.Fatal("agents service is unavailable")
		}
		handle, err := agentRegistry.Create(requestContext, pluginScope, agentcore.CreateOptions{
			SessionID: "durable-session", Metadata: session.Metadata{CWD: &workspace},
			AgentOptions: agentcore.Options{Provider: "deepseek-official", Model: "deepseek-v4-flash"},
		})
		if err != nil {
			return err
		}
		conversation := handle.Subject.SessionValue()
		if _, err := session.Append(conversation, session.TurnStarted, session.TurnStart{Turn: 1}); err != nil {
			return err
		}
		if _, err := session.Append(conversation, session.TurnEnded, session.TurnEnd{
			Turn: 1, Reason: session.TurnCompleted{},
		}); err != nil {
			return err
		}
		store, found := plugin.Require(pluginScope, session.StoreService)
		if !found {
			t.Fatal("sessions service is unavailable")
		}
		if _, err := store.Flush(requestContext, conversation); err != nil {
			return err
		}
		return handle.Dispose(requestContext)
	}}
	if _, err := firstRuntime.Load(requestContext, firstProbe); err != nil {
		t.Fatal(err)
	}
	if err := firstRuntime.Shutdown(requestContext); err != nil {
		t.Fatal(err)
	}

	secondCatalog, err := NewCatalog(Environment{WorkingDirectory: workspace})
	if err != nil {
		t.Fatal(err)
	}
	secondSpecs, err := DefaultSpecs("127.0.0.1:0", "test", databasePath, workspaceDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	secondRuntime := plugin.NewRuntime()
	if _, err := Load(requestContext, secondRuntime, secondCatalog, secondSpecs); err != nil {
		t.Fatal(err)
	}
	serverAddress := ""
	var agentRegistry agentcore.Registry
	secondProbe := probePlugin{body: func(_ context.Context, pluginScope *plugin.Scope) error {
		serverEndpoint, found := plugin.Require(pluginScope, serverServiceKey)
		if !found {
			t.Fatal("webServer service is unavailable")
		}
		serverAddress = serverEndpoint.Address()
		agentRegistry, found = plugin.Require(pluginScope, agentcore.Service)
		if !found {
			t.Fatal("agents service is unavailable")
		}
		return nil
	}}
	if _, err := secondRuntime.Load(requestContext, secondProbe); err != nil {
		t.Fatal(err)
	}

	listResult := callSessionUnary(t, serverAddress, apiproxy.SessionListMethod, "list-1", `{}`)
	var listed apiproxy.SessionListValue
	if err := json.Unmarshal(listResult.Value, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 || listed.Items[0].SessionID != "durable-session" || listed.Items[0].Running {
		t.Fatalf("cold session.list = %#v", listed)
	}
	historyResult := callSessionUnary(
		t, serverAddress, apiproxy.SessionHistoryMethod, "history-1",
		`{"sessionId":"durable-session"}`,
	)
	var history apiproxy.SessionHistoryValue
	if err := json.Unmarshal(historyResult.Value, &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Events) != 2 || history.Events[0].Event.Type != session.TurnStartEventName ||
		history.Events[1].Event.Type != session.TurnEndEventName {
		t.Fatalf("cold session.history = %#v", history)
	}
	createPayload, err := json.Marshal(struct {
		SessionID string `json:"sessionId"`
		CWD       string `json:"cwd"`
	}{SessionID: "durable-session", CWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	createResult := callSessionUnary(
		t, serverAddress, apiproxy.SessionCreateMethod, "create-1", string(createPayload),
	)
	var created apiproxy.SessionCreateValue
	if err := json.Unmarshal(createResult.Value, &created); err != nil {
		t.Fatal(err)
	}
	resumed, found := agentRegistry.Get("durable-session")
	if created.SessionID != "durable-session" || !found || resumed.SessionValue().FirstLiveSeq() != 2 ||
		len(resumed.SessionValue().Events()) != 3 || resumed.SessionValue().Events()[2].Type != session.EndSeedEventName {
		t.Fatalf("resumed Session = (%#v, %t, %#v)", created, found, resumed)
	}
	closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := secondRuntime.Shutdown(closeContext); err != nil {
		t.Fatal(err)
	}
}

func callSessionUnary(
	t *testing.T,
	serverAddress string,
	method string,
	rpcID string,
	payload string,
) protocol.RPCResult {
	t.Helper()
	encoded := []byte(`{"type":"client-request","rpcId":"` + rpcID + `","method":"` + method + `","payload":` + payload + `}`)
	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost, "http://"+serverAddress+protocol.APIPath+"/"+method, bytes.NewReader(encoded),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var message protocol.ServerResponse
	if err := json.NewDecoder(response.Body).Decode(&message); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !message.Result.OK {
		t.Fatalf("%s response = (%d, %#v)", method, response.StatusCode, message)
	}
	return message.Result
}
