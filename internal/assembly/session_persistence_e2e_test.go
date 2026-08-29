package assembly

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/apiproxy"
	protocol "github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	projectioncachesqlite "github.com/gorenx/goren/session/projectioncache/sqlite"
)

func TestDefaultCompositionListsHistoryAndResumesAColdSQLiteSession(t *testing.T) {
	requestContext := context.Background()
	workingDirectory := t.TempDir()
	dataDirectory := t.TempDir()
	databasePath := dataDirectory + "/sessions.sqlite"
	workspaceDatabasePath := dataDirectory + "/workspaces.sqlite"

	firstCatalog := newTestCatalog(t, workingDirectory)
	firstSpecs, err := DefaultSpecs(
		"127.0.0.1:0",
		"test",
		databasePath,
		workspaceDatabasePath,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstProbe, firstSpecs := addProbe(t, firstCatalog, firstSpecs)
	firstServer, err := BuildServer(requestContext, firstCatalog, firstSpecs)
	if err != nil {
		t.Fatal(err)
	}
	firstRuntime := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: testDiagnostics(t),
	})
	if _, err = firstRuntime.Start(requestContext, firstServer); err != nil {
		t.Fatal(err)
	}
	firstHandle, err := firstProbe.constructor.Create(
		requestContext,
		agent.CreateOptions{
			SessionID: "durable-session",
			Metadata: session.Metadata{
				CWD: &workingDirectory,
			},
			AgentOptions: agent.Options{
				Provider: deepseek.ProviderRoute,
				Model:    deepseek.DefaultModelID,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	conversation := firstHandle.Subject.SessionValue()
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewEventDraft(session.TurnStarted,
			session.TurnStart{
				Turn: 1,
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		if _, err = committedEvent, writeErr; err != nil {
			t.Fatal(err)
		}
	}
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewEventDraft(session.TurnEnded,
			session.TurnEnd{
				Turn:   1,
				Reason: session.TurnCompleted{},
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		if _, err = committedEvent, writeErr; err != nil {
			t.Fatal(err)
		}
	}
	if err = firstProbe.sessions.Flush(requestContext, conversation); err != nil {
		t.Fatal(err)
	}
	if err = firstHandle.Dispose(requestContext); err != nil {
		t.Fatal(err)
	}
	if err = firstRuntime.Shutdown(requestContext); err != nil {
		t.Fatal(err)
	}
	checkpointStore, err := projectioncachesqlite.Open(
		requestContext,
		projectioncachesqlite.Config{
			Path:        dataDirectory + "/session-projection-cache.sqlite",
			JournalMode: projectioncachesqlite.JournalWAL,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	checkpointRecords, err := checkpointStore.LoadAll(requestContext)
	if err != nil {
		t.Fatal(err)
	}
	checkpointRecord, found := checkpointRecords["durable-session"]
	if !found || checkpointRecord.Rows["sessionListMetadata"].Seq != 1 {
		t.Fatalf("durable projection checkpoint = %#v", checkpointRecords)
	}
	if err := checkpointStore.Close(requestContext); err != nil {
		t.Fatal(err)
	}

	secondCatalog := newTestCatalog(t, workingDirectory)
	secondSpecs, err := DefaultSpecs(
		"127.0.0.1:0",
		"test",
		databasePath,
		workspaceDatabasePath,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondProbe, secondSpecs := addProbe(t, secondCatalog, secondSpecs)
	secondServer, err := BuildServer(requestContext, secondCatalog, secondSpecs)
	if err != nil {
		t.Fatal(err)
	}
	secondRuntime := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: testDiagnostics(t),
	})
	if _, err = secondRuntime.Start(requestContext, secondServer); err != nil {
		t.Fatal(err)
	}

	serverAddress := secondServer.BoundAddress()
	listResult := callSessionUnary(
		t,
		serverAddress,
		apiproxy.SessionListMethod,
		"list-1",
		`{}`,
	)
	var listed apiproxy.SessionListValue
	if err = json.Unmarshal(listResult.Value, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 ||
		listed.Items[0].SessionID != "durable-session" ||
		listed.Items[0].Running {
		t.Fatalf("cold session.list = %#v", listed)
	}
	historyResult := callSessionUnary(
		t,
		serverAddress,
		apiproxy.SessionHistoryMethod,
		"history-1",
		`{"sessionId":"durable-session"}`,
	)
	var history apiproxy.SessionHistoryValue
	if err = json.Unmarshal(historyResult.Value, &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Events) != 2 ||
		history.Events[0].Event.Type != session.TurnStartEventName ||
		history.Events[1].Event.Type != session.TurnEndEventName ||
		history.Projections == nil || history.Projections.AsOfSeq != 1 {
		t.Fatalf("cold session.history = %#v", history)
	}
	createPayload, err := json.Marshal(struct {
		SessionID string `json:"sessionId"`
		CWD       string `json:"cwd"`
	}{
		SessionID: "durable-session",
		CWD:       workingDirectory,
	})
	if err != nil {
		t.Fatal(err)
	}
	createResult := callSessionUnary(
		t,
		serverAddress,
		apiproxy.SessionCreateMethod,
		"create-1",
		string(createPayload),
	)
	var created apiproxy.SessionCreateValue
	if err = json.Unmarshal(createResult.Value, &created); err != nil {
		t.Fatal(err)
	}
	resumed, found := secondProbe.agents.Get("durable-session")
	if created.SessionID != "durable-session" || !found ||
		resumed.SessionValue().FirstLiveSeq() != 2 ||
		len(resumed.SessionValue().Events()) != 3 ||
		resumed.SessionValue().Events()[2].Type != session.EndSeedEventName {
		t.Fatalf(
			"resumed Session = (%#v, %t, %#v)",
			created,
			found,
			resumed,
		)
	}
	closeContext, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()
	if err = secondRuntime.Shutdown(closeContext); err != nil {
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
	encoded := []byte(
		`{"type":"client-request","rpcId":"` + rpcID +
			`","method":"` + method + `","payload":` + payload + `}`,
	)
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"http://"+serverAddress+protocol.APIPath+"/"+method,
		bytes.NewReader(encoded),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := (&http.Client{
		Timeout: 3 * time.Second,
	}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var message protocol.ServerResponse
	if err = json.NewDecoder(response.Body).Decode(&message); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !message.Result.OK {
		t.Fatalf(
			"%s response = (%d, %#v)",
			method,
			response.StatusCode,
			message,
		)
	}
	return message.Result
}
