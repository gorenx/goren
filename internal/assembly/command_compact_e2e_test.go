package assembly

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/compaction/basic"
	"github.com/gorenx/goren/connection"
	connectionhost "github.com/gorenx/goren/internal/connection"
	"github.com/gorenx/goren/session"
)

func TestDefaultCompositionExecutesCompactCommandThroughHTTP(t *testing.T) {
	var mainRequests atomic.Int32
	var compactRequests atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
			defer httpRequest.Body.Close()
			if httpRequest.Header.Get("x-deepseek-harness-compact") == "1" {
				compactRequests.Add(1)
				writeCompactionFixtureSSE(
					responseWriter,
					"manual command checkpoint",
					200,
				)
				return
			}
			mainRequests.Add(1)
			writeCompactionFixtureSSE(responseWriter, "main response", 2_000)
		},
	))
	defer providerServer.Close()

	automatic := false
	runtimeEngine, serviceView, assembledServer := startCompactionFixtureComposition(
		t,
		providerServer.URL,
		12_500,
		basic.Config{
			Auto: &automatic,
		},
		t.TempDir(),
		testDiagnostics(t),
	)
	defer shutdownCompactionFixtureComposition(t, runtimeEngine)
	handle := createCompactionFixtureAgent(t, serviceView, "command-compact-agent")
	defer disposeCompactionFixtureAgent(t, handle)

	sendCompactionFixturePrompt(
		t,
		handle.Subject,
		strings.Repeat("older manual history ", 100),
	)
	sendCompactionFixturePrompt(
		t,
		handle.Subject,
		strings.Repeat("newer retained history ", 100),
	)
	beforeEvents := len(handle.Subject.SessionValue().Events())
	beforeGeneration := handle.Subject.SessionValue().Surface().ReplaceGeneration

	listOutcome := callCommandHTTP(
		t,
		assembledServer.BoundAddress(),
		"commands-list",
		"commands/list",
		json.RawMessage(`{"args":{"agentId":"command-compact-agent"}}`),
	)
	if !listOutcome.OK {
		t.Fatalf("commands/list = %#v", listOutcome)
	}
	var descriptors []commands.Descriptor
	if err := json.Unmarshal(listOutcome.Value, &descriptors); err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 1 || descriptors[0].Name != "compact" ||
		descriptors[0].Description != "Compact older conversation history" {
		t.Fatalf("default Commands directory = %#v", descriptors)
	}

	executeOutcome := callCommandHTTP(
		t,
		assembledServer.BoundAddress(),
		"commands-execute",
		"commands/execute",
		json.RawMessage(
			`{"args":{"agentId":"command-compact-agent","line":"/compact","images":[]}}`,
		),
	)
	if !executeOutcome.OK {
		t.Fatalf("commands/execute = %#v", executeOutcome)
	}
	var settled commands.Execution
	if err := json.Unmarshal(executeOutcome.Value, &settled); err != nil {
		t.Fatal(err)
	}
	if settled.Result.Kind != commands.ResultSuccess || settled.Result.Text == nil ||
		!strings.HasPrefix(*settled.Result.Text, "Compacted ") ||
		settled.Result.SourceEventSeq == nil {
		t.Fatalf("/compact execution = %#v", settled)
	}

	conversation := handle.Subject.SessionValue()
	entries := conversation.Events()
	if len(entries) != beforeEvents+6 {
		t.Fatalf(
			"manual command appended %d events, want 6; tail = %#v",
			len(entries)-beforeEvents,
			entries[beforeEvents:],
		)
	}
	wantTypes := []string{
		commands.RunEventName,
		compaction.StartEventName,
		compaction.SummaryEventName,
		session.UserMessageEventName,
		compaction.EndEventName,
		commands.DoneEventName,
	}
	for offset, wantType := range wantTypes {
		if entries[beforeEvents+offset].Type != wantType {
			t.Fatalf("manual lifecycle types = %#v", eventTypes(entries[beforeEvents:]))
		}
	}
	var runValue commands.Run
	if err := json.Unmarshal(entries[beforeEvents].Data, &runValue); err != nil {
		t.Fatal(err)
	}
	startValue, err := compaction.DecodeStart(entries[beforeEvents+1].Data)
	if err != nil {
		t.Fatal(err)
	}
	summaryValue, err := compaction.DecodeSummary(entries[beforeEvents+2].Data)
	if err != nil {
		t.Fatal(err)
	}
	var doneValue commands.Done
	if err := json.Unmarshal(entries[beforeEvents+5].Data, &doneValue); err != nil {
		t.Fatal(err)
	}
	if runValue.CommandID != settled.CommandID || runValue.Name != "compact" ||
		runValue.Args == nil || *runValue.Args != "" ||
		startValue.SourceCommandID == nil || *startValue.SourceCommandID != string(settled.CommandID) ||
		summaryValue.SourceCommandID == nil || *summaryValue.SourceCommandID != string(settled.CommandID) ||
		doneValue.CommandID != settled.CommandID || doneValue.SourceEventSeq == nil ||
		*doneValue.SourceEventSeq != entries[beforeEvents+2].Seq ||
		*settled.Result.SourceEventSeq != entries[beforeEvents+2].Seq {
		t.Fatalf(
			"manual command provenance = run %#v start %#v summary %#v done %#v execution %#v",
			runValue,
			startValue,
			summaryValue,
			doneValue,
			settled,
		)
	}
	if conversation.Surface().ReplaceGeneration <= beforeGeneration {
		t.Fatalf(
			"manual replacement generation = %d, want greater than %d",
			conversation.Surface().ReplaceGeneration,
			beforeGeneration,
		)
	}
	if mainRequests.Load() != 2 || compactRequests.Load() != 1 {
		t.Fatalf(
			"provider requests = main %d compact %d, want main 2 compact 1",
			mainRequests.Load(),
			compactRequests.Load(),
		)
	}
	if err := serviceView.sessions.Flush(context.Background(), conversation); err != nil {
		t.Fatal(err)
	}
	stored, err := serviceView.durability.Inspect(
		context.Background(),
		conversation.ID(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Events) != len(entries) ||
		stored.Events[beforeEvents].Type != commands.RunEventName ||
		stored.Events[len(stored.Events)-1].Type != commands.DoneEventName {
		t.Fatalf(
			"durable command lifecycle = %v, want %v events ending in command/done",
			eventTypes(stored.Events),
			len(entries),
		)
	}
}

func TestDefaultCompositionMapsCommandFailuresThroughHTTP(t *testing.T) {
	var providerRequests atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
			providerRequests.Add(1)
			defer httpRequest.Body.Close()
			writeCompactionFixtureSSE(responseWriter, "unexpected", 1)
		},
	))
	defer providerServer.Close()

	automatic := false
	runtimeEngine, serviceView, assembledServer := startCompactionFixtureComposition(
		t,
		providerServer.URL,
		12_500,
		basic.Config{
			Auto: &automatic,
		},
		t.TempDir(),
		testDiagnostics(t),
	)
	defer shutdownCompactionFixtureComposition(t, runtimeEngine)
	handle := createCompactionFixtureAgent(t, serviceView, "command-failure-agent")
	defer disposeCompactionFixtureAgent(t, handle)

	invalidOutcome := callCommandHTTP(
		t,
		assembledServer.BoundAddress(),
		"commands-invalid",
		"commands/execute",
		json.RawMessage(
			`{"args":{"agentId":"command-failure-agent","line":"/compact"}}`,
		),
	)
	if invalidOutcome.OK || invalidOutcome.Error == nil ||
		invalidOutcome.Error.Code != connection.ErrorInternal {
		t.Fatalf("invalid Commands Remote payload = %#v", invalidOutcome)
	}

	missingOutcome := callCommandHTTP(
		t,
		assembledServer.BoundAddress(),
		"commands-missing-agent",
		"commands/list",
		json.RawMessage(`{"args":{"agentId":"missing-command-agent"}}`),
	)
	if missingOutcome.OK || missingOutcome.Error == nil ||
		missingOutcome.Error.Code != connection.ErrorSessionNotFound {
		t.Fatalf("missing Commands Agent = %#v", missingOutcome)
	}
	var missingDetails struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(missingOutcome.Error.Details, &missingDetails); err != nil {
		t.Fatal(err)
	}
	if missingDetails.SessionID != "missing-command-agent" {
		t.Fatalf("missing Commands Agent details = %#v", missingDetails)
	}

	beforeEvents := len(handle.Subject.SessionValue().Events())
	imageOutcome := callCommandHTTP(
		t,
		assembledServer.BoundAddress(),
		"commands-image-rejection",
		"commands/execute",
		json.RawMessage(
			`{"args":{"agentId":"command-failure-agent","line":"/compact","images":[{"mediaType":"image/png","data":"AA=="}]}}`,
		),
	)
	if !imageOutcome.OK {
		t.Fatalf("image-rejected Commands execution = %#v", imageOutcome)
	}
	var imageExecution commands.Execution
	if err := json.Unmarshal(imageOutcome.Value, &imageExecution); err != nil {
		t.Fatal(err)
	}
	if imageExecution.Result.Kind != commands.ResultError ||
		imageExecution.Result.Text == nil ||
		*imageExecution.Result.Text != "/compact does not accept image attachments" {
		t.Fatalf("image-rejected Commands result = %#v", imageExecution)
	}
	entries := handle.Subject.SessionValue().Events()
	if len(entries) != beforeEvents+2 ||
		entries[beforeEvents].Type != commands.RunEventName ||
		entries[beforeEvents+1].Type != commands.DoneEventName {
		t.Fatalf("image-rejected Commands lifecycle = %#v", entries[beforeEvents:])
	}
	if providerRequests.Load() != 0 {
		t.Fatalf("rejected Commands invoked Provider %d times", providerRequests.Load())
	}
}

func TestDefaultCompositionCancelsCompactCommandThroughHTTP(t *testing.T) {
	compactStarted := make(chan struct{})
	compactCancelled := make(chan struct{})
	var mainRequests atomic.Int32
	var compactRequests atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
			defer httpRequest.Body.Close()
			if httpRequest.Header.Get("x-deepseek-harness-compact") == "1" {
				compactRequests.Add(1)
				responseWriter.Header().Set("Content-Type", "text/event-stream")
				responseWriter.WriteHeader(http.StatusOK)
				if flusher, available := responseWriter.(http.Flusher); available {
					flusher.Flush()
				}
				close(compactStarted)
				<-httpRequest.Context().Done()
				close(compactCancelled)
				return
			}
			mainRequests.Add(1)
			writeCompactionFixtureSSE(responseWriter, "main response", 2_000)
		},
	))
	defer func() {
		providerServer.CloseClientConnections()
		providerServer.Close()
	}()

	automatic := false
	runtimeEngine, serviceView, _ := startCompactionFixtureComposition(
		t,
		providerServer.URL,
		12_500,
		basic.Config{
			Auto: &automatic,
		},
		t.TempDir(),
		testDiagnostics(t),
	)
	defer shutdownCompactionFixtureComposition(t, runtimeEngine)
	handle := createCompactionFixtureAgent(t, serviceView, "command-cancel-agent")
	defer disposeCompactionFixtureAgent(t, handle)
	sendCompactionFixturePrompt(
		t,
		handle.Subject,
		strings.Repeat("older cancellation history ", 100),
	)
	sendCompactionFixturePrompt(
		t,
		handle.Subject,
		strings.Repeat("newer cancellation history ", 100),
	)
	conversation := handle.Subject.SessionValue()
	beforeEvents := len(conversation.Events())
	carrier, err := connectionhost.NewHTTPHost(
		connectionhost.HTTPConfig{},
		serviceView.apiProxy,
		serviceView.apiProxy,
	)
	if err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(connection.ClientRequest{
		Type:   connection.ClientRequestType,
		RPCID:  "commands-cancel",
		Method: "commands/execute",
		Payload: json.RawMessage(
			`{"args":{"agentId":"command-cancel-agent","line":"/compact","images":[]}}`,
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	httpRequest := httptest.NewRequest(
		http.MethodPost,
		"http://localhost"+connection.APIPath+"/commands/execute",
		bytes.NewReader(encoded),
	).WithContext(requestContext)
	httpRequest.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		carrier.ServeHTTP(recorder, httpRequest)
	}()

	waitCommandSignal(t, compactStarted, "compact Provider request")
	cancelRequest()
	waitCommandSignal(t, requestDone, "cancelled Commands HTTP request")
	if recorder.Code != http.StatusOK {
		t.Fatalf("cancelled Commands HTTP status = %d", recorder.Code)
	}
	var cancelledEnvelope connection.ServerResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &cancelledEnvelope); err != nil {
		t.Fatal(err)
	}
	if cancelledEnvelope.Result.OK || cancelledEnvelope.Result.Error == nil ||
		cancelledEnvelope.Result.Error.Code != connection.ErrorCancelled {
		t.Fatalf("cancelled Commands HTTP outcome = %#v", cancelledEnvelope.Result)
	}
	waitCommandSignal(t, compactCancelled, "compact Provider cancellation")
	entries := waitCommandCancellationLifecycle(t, conversation, beforeEvents)

	var runValue commands.Run
	var doneValue commands.Done
	var startValue compaction.Start
	var endValue compaction.End
	runIndex := -1
	doneIndex := -1
	startIndex := -1
	endIndex := -1
	for entryIndex, entry := range entries {
		switch entry.Type {
		case commands.RunEventName:
			runIndex = entryIndex
			if err := json.Unmarshal(entry.Data, &runValue); err != nil {
				t.Fatal(err)
			}
		case commands.DoneEventName:
			doneIndex = entryIndex
			if err := json.Unmarshal(entry.Data, &doneValue); err != nil {
				t.Fatal(err)
			}
		case compaction.StartEventName:
			startIndex = entryIndex
			startValue, err = compaction.DecodeStart(entry.Data)
			if err != nil {
				t.Fatal(err)
			}
		case compaction.EndEventName:
			endIndex = entryIndex
			endValue, err = compaction.DecodeEnd(entry.Data)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if runIndex < 0 || startIndex <= runIndex || doneIndex <= runIndex ||
		endIndex <= startIndex {
		t.Fatalf("cancelled lifecycle order = %#v", eventTypes(entries))
	}
	if doneValue.CommandID != runValue.CommandID ||
		doneValue.Kind != commands.ResultError || doneValue.Text == nil ||
		*doneValue.Text != context.Canceled.Error() ||
		startValue.SourceCommandID == nil ||
		*startValue.SourceCommandID != string(runValue.CommandID) ||
		endValue.SourceCommandID == nil ||
		*endValue.SourceCommandID != string(runValue.CommandID) ||
		endValue.Error == nil {
		t.Fatalf(
			"cancelled lifecycle values = run %#v done %#v start %#v end %#v",
			runValue,
			doneValue,
			startValue,
			endValue,
		)
	}
	state, err := compaction.InspectLog(conversation.Events())
	if err != nil || state.Attempt != nil {
		t.Fatalf("cancelled compaction state = %#v, error = %v", state, err)
	}
	if mainRequests.Load() != 2 || compactRequests.Load() != 1 {
		t.Fatalf(
			"cancelled Provider requests = main %d compact %d",
			mainRequests.Load(),
			compactRequests.Load(),
		)
	}
}

func callCommandHTTP(
	testingContext *testing.T,
	address string,
	correlationID connection.RPCID,
	method string,
	payload json.RawMessage,
) connection.RPCResult {
	testingContext.Helper()
	encoded, err := json.Marshal(connection.ClientRequest{
		Type:    connection.ClientRequestType,
		RPCID:   correlationID,
		Method:  method,
		Payload: payload,
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	response, err := http.Post(
		"http://"+address+connection.APIPath+"/"+method,
		"application/json",
		bytes.NewReader(encoded),
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		testingContext.Fatal(errors.Join(readErr, closeErr))
	}
	if response.StatusCode != http.StatusOK {
		testingContext.Fatalf(
			"POST %s status = %d body = %s",
			method,
			response.StatusCode,
			body,
		)
	}
	var envelope connection.ServerResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		testingContext.Fatal(err)
	}
	if envelope.RPCID != correlationID {
		testingContext.Fatalf(
			"POST %s rpcId = %q, want %q",
			method,
			envelope.RPCID,
			correlationID,
		)
	}
	return envelope.Result
}

func eventTypes(entries []session.Event) []string {
	names := make([]string, len(entries))
	for entryIndex, entry := range entries {
		names[entryIndex] = entry.Type
	}
	return names
}

func waitCommandSignal(
	testingContext *testing.T,
	signal <-chan struct{},
	description string,
) {
	testingContext.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		testingContext.Fatalf("timed out waiting for %s", description)
	}
}

func waitCommandCancellationLifecycle(
	testingContext *testing.T,
	conversation *session.Session,
	startIndex int,
) []session.Event {
	testingContext.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		entries := conversation.Events()
		tail := entries[startIndex:]
		seenDone := false
		seenEnd := false
		for _, entry := range tail {
			seenDone = seenDone || entry.Type == commands.DoneEventName
			seenEnd = seenEnd || entry.Type == compaction.EndEventName
		}
		if seenDone && seenEnd {
			return tail
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			testingContext.Fatalf(
				"timed out waiting for cancelled lifecycle; tail = %#v",
				tail,
			)
			return nil
		}
	}
}
