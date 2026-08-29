package assembly

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/compaction/basic"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/title"
)

func TestDefaultCompositionCompactsPressureThroughAgentLoop(t *testing.T) {
	var mainRequests atomic.Int32
	var compactRequests atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
			defer httpRequest.Body.Close()
			if httpRequest.Header.Get("x-deepseek-harness-compact") == "1" {
				compactRequests.Add(1)
				writeCompactionFixtureSSE(
					responseWriter,
					"small durable checkpoint",
					200,
				)
				return
			}
			requestNumber := mainRequests.Add(1)
			inputTokens := int64(4_500)
			if requestNumber >= 2 {
				inputTokens = 9_000
			}
			writeCompactionFixtureSSE(
				responseWriter,
				"main response",
				inputTokens,
			)
		},
	))
	defer providerServer.Close()

	runtimeEngine, serviceView, _ := startCompactionFixtureComposition(
		t,
		providerServer.URL,
		12_500,
		basic.Config{},
		t.TempDir(),
		testDiagnostics(t),
	)
	defer shutdownCompactionFixtureComposition(t, runtimeEngine)
	handle := createCompactionFixtureAgent(t, serviceView, "pressure-agent")
	defer disposeCompactionFixtureAgent(t, handle)

	sendCompactionFixturePrompt(
		t,
		handle.Subject,
		strings.Repeat("first pressure history ", 800),
	)
	firstMeasurement, err := serviceView.meter.Measure(
		context.Background(),
		handle.Subject.SessionValue(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if firstMeasurement.TotalTokens >= 10_000 {
		t.Fatalf(
			"first pressure measurement = %d, want below threshold",
			firstMeasurement.TotalTokens,
		)
	}
	sendCompactionFixturePrompt(
		t,
		handle.Subject,
		strings.Repeat("second pressure history ", 800),
	)
	secondMeasurement, err := serviceView.meter.Measure(
		context.Background(),
		handle.Subject.SessionValue(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if secondMeasurement.TotalTokens < 10_000 {
		t.Fatalf(
			"second pressure measurement = %d, want at least threshold",
			secondMeasurement.TotalTokens,
		)
	}
	sendCompactionFixturePrompt(t, handle.Subject, "continue after pressure")

	entries := handle.Subject.SessionValue().Events()
	assertCompactionFixtureTransaction(t, entries)
	if mainRequests.Load() != 3 || compactRequests.Load() != 1 {
		t.Fatalf(
			"pressure request counts = main %d, compact %d",
			mainRequests.Load(),
			compactRequests.Load(),
		)
	}
}

func TestDefaultCompositionRetriesOverflowAfterSurfaceReplacement(t *testing.T) {
	var mainRequests atomic.Int32
	var compactRequests atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
			defer httpRequest.Body.Close()
			if httpRequest.Header.Get("x-deepseek-harness-compact") == "1" {
				compactRequests.Add(1)
				writeCompactionFixtureSSE(
					responseWriter,
					"overflow recovery checkpoint",
					200,
				)
				return
			}
			requestNumber := mainRequests.Add(1)
			if requestNumber == 2 {
				responseWriter.Header().Set("content-type", "application/json")
				responseWriter.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprint(
					responseWriter,
					`{"error":{"message":"request too large for model context"}}`,
				)
				return
			}
			writeCompactionFixtureSSE(responseWriter, "main response", 5_000)
		},
	))
	defer providerServer.Close()

	thresholdRatio := float64(1)
	runtimeEngine, serviceView, _ := startCompactionFixtureComposition(
		t,
		providerServer.URL,
		12_500,
		basic.Config{
			PolicyConfig: basic.PolicyConfig{
				ThresholdRatio: &thresholdRatio,
			},
		},
		t.TempDir(),
		testDiagnostics(t),
	)
	defer shutdownCompactionFixtureComposition(t, runtimeEngine)
	handle := createCompactionFixtureAgent(t, serviceView, "overflow-agent")
	defer disposeCompactionFixtureAgent(t, handle)

	sendCompactionFixturePrompt(
		t,
		handle.Subject,
		strings.Repeat("overflow history that must remain shrinkable ", 100),
	)
	beforeGeneration := handle.Subject.SessionValue().Surface().ReplaceGeneration
	sendCompactionFixturePrompt(
		t,
		handle.Subject,
		"the provider rejects this request once",
	)
	afterGeneration := handle.Subject.SessionValue().Surface().ReplaceGeneration

	entries := handle.Subject.SessionValue().Events()
	assertCompactionFixtureTransaction(t, entries)
	if afterGeneration <= beforeGeneration {
		t.Fatalf(
			"overflow replacement generation = %d, want greater than %d",
			afterGeneration,
			beforeGeneration,
		)
	}
	if mainRequests.Load() != 3 || compactRequests.Load() != 1 {
		t.Fatalf(
			"overflow request counts = main %d, compact %d",
			mainRequests.Load(),
			compactRequests.Load(),
		)
	}
	if kind := latestCompactionFixtureTurnEndKind(t, entries); kind != "completed" {
		t.Fatalf("overflow recovery turn end kind = %q", kind)
	}
}

func TestDefaultCompositionNeverRetriesOverflowAfterCancellation(t *testing.T) {
	var mainRequests atomic.Int32
	var compactRequests atomic.Int32
	compactStarted := make(chan struct{})
	compactCancelled := make(chan struct{})
	providerServer := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
			defer httpRequest.Body.Close()
			if httpRequest.Header.Get("x-deepseek-harness-compact") == "1" {
				compactRequests.Add(1)
				responseWriter.Header().Set("content-type", "text/event-stream")
				responseWriter.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(
					responseWriter,
					"data: {\"choices\":[{\"delta\":{\"content\":\"partial summary\"}}]}\n\n",
				)
				if flusher, supported := responseWriter.(http.Flusher); supported {
					flusher.Flush()
				}
				close(compactStarted)
				<-httpRequest.Context().Done()
				close(compactCancelled)
				return
			}
			requestNumber := mainRequests.Add(1)
			if requestNumber == 2 {
				responseWriter.Header().Set("content-type", "application/json")
				responseWriter.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprint(
					responseWriter,
					`{"error":{"message":"request too large for model context"}}`,
				)
				return
			}
			writeCompactionFixtureSSE(responseWriter, "main response", 5_000)
		},
	))
	defer providerServer.Close()

	diagnosticProblems := make(chan error, 4)
	diagnosticSink, err := NewDiagnostics(func(problem error) {
		diagnosticProblems <- problem
	})
	if err != nil {
		t.Fatal(err)
	}
	thresholdRatio := float64(1)
	runtimeEngine, serviceView, _ := startCompactionFixtureComposition(
		t,
		providerServer.URL,
		12_500,
		basic.Config{
			PolicyConfig: basic.PolicyConfig{
				ThresholdRatio: &thresholdRatio,
			},
		},
		t.TempDir(),
		diagnosticSink,
	)
	defer shutdownCompactionFixtureComposition(t, runtimeEngine)
	handle := createCompactionFixtureAgent(
		t,
		serviceView,
		"cancelled-overflow-agent",
	)
	defer disposeCompactionFixtureAgent(t, handle)

	sendCompactionFixturePrompt(
		t,
		handle.Subject,
		strings.Repeat("overflow cancellation history ", 100),
	)
	beforeGeneration := handle.Subject.SessionValue().Surface().ReplaceGeneration
	if err := handle.Subject.Followup(compactionFixtureUserMessage(
		t,
		"cancel the overflow recovery",
	)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-compactStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("overflow compaction stream did not start")
	}
	handle.Subject.Cancel(
		agent.UserCancel{},
		agent.CancelOptions{
			KeepInbox: true,
		},
	)
	waitCompactionFixtureAgentIdle(t, handle.Subject)
	select {
	case <-compactCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled summary stream remained open")
	}

	conversation := handle.Subject.SessionValue()
	afterGeneration := conversation.Surface().ReplaceGeneration
	if afterGeneration != beforeGeneration {
		t.Fatalf(
			"cancelled overflow replacement generation = %d, want %d",
			afterGeneration,
			beforeGeneration,
		)
	}
	if mainRequests.Load() != 2 || compactRequests.Load() != 1 {
		t.Fatalf(
			"cancelled overflow request counts = main %d, compact %d",
			mainRequests.Load(),
			compactRequests.Load(),
		)
	}
	entries := conversation.Events()
	state, inspectErr := compaction.InspectLog(entries)
	if inspectErr != nil || state.Attempt != nil {
		t.Fatalf("cancelled compaction state = %#v, error = %v", state, inspectErr)
	}
	if kind := latestCompactionFixtureTurnEndKind(t, entries); kind != "aborted" {
		t.Fatalf("cancelled overflow turn end kind = %q", kind)
	}
	select {
	case problem := <-diagnosticProblems:
		if !strings.Contains(
			problem.Error(),
			"context-overflow compaction failed",
		) {
			t.Fatalf("cancelled overflow diagnostic = %v", problem)
		}
	default:
		t.Fatal("cancelled overflow failure was not reported")
	}
}

func TestDefaultCompositionRestoresCompactionAcrossSQLiteRestart(t *testing.T) {
	var mainRequests atomic.Int32
	var compactRequests atomic.Int32
	requestBodies := make(chan string, 8)
	providerServer := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
			defer httpRequest.Body.Close()
			if httpRequest.Header.Get("x-deepseek-harness-compact") == "1" {
				compactRequests.Add(1)
				writeCompactionFixtureSSE(
					responseWriter,
					"cold restart checkpoint",
					200,
				)
				return
			}
			bodyValue, err := io.ReadAll(httpRequest.Body)
			if err != nil {
				http.Error(responseWriter, err.Error(), http.StatusBadRequest)
				return
			}
			requestBodies <- string(bodyValue)
			requestNumber := mainRequests.Add(1)
			inputTokens := int64(4_500)
			if requestNumber >= 2 {
				inputTokens = 9_000
			}
			writeCompactionFixtureSSE(
				responseWriter,
				"main response",
				inputTokens,
			)
		},
	))
	defer providerServer.Close()

	dataDirectory := t.TempDir()
	diagnosticSink := testDiagnostics(t)
	firstRuntime, firstServices, _ := startCompactionFixtureComposition(
		t,
		providerServer.URL,
		12_500,
		basic.Config{},
		dataDirectory,
		diagnosticSink,
	)
	firstHandle := createCompactionFixtureAgent(
		t,
		firstServices,
		"cold-compaction-agent",
	)
	firstPayload := strings.Repeat("first pressure history ", 800)
	sendCompactionFixturePrompt(t, firstHandle.Subject, firstPayload)
	waitCompactionFixtureRequestBody(t, requestBodies)
	sendCompactionFixturePrompt(
		t,
		firstHandle.Subject,
		strings.Repeat("second pressure history ", 800),
	)
	waitCompactionFixtureRequestBody(t, requestBodies)
	sendCompactionFixturePrompt(t, firstHandle.Subject, "commit the checkpoint")
	compactedRequest := waitCompactionFixtureRequestBody(t, requestBodies)
	if !strings.Contains(compactedRequest, "cold restart checkpoint") ||
		strings.Contains(compactedRequest, firstPayload) {
		t.Fatalf("request after compaction did not use the checkpoint surface")
	}

	firstConversation := firstHandle.Subject.SessionValue()
	firstEntries := firstConversation.Events()
	assertCompactionFixtureTransaction(t, firstEntries)
	firstSurface := firstConversation.Surface()
	firstMeasurement, err := firstServices.meter.Measure(
		context.Background(),
		firstConversation,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstProjection, err := firstServices.projections.Snapshot(firstConversation)
	if err != nil {
		t.Fatal(err)
	}
	if err = firstServices.sessions.Flush(context.Background(), firstConversation); err != nil {
		t.Fatal(err)
	}
	disposeCompactionFixtureAgent(t, firstHandle)
	shutdownCompactionFixtureComposition(t, firstRuntime)

	thresholdRatio := float64(1)
	secondRuntime, secondServices, _ := startCompactionFixtureComposition(
		t,
		providerServer.URL,
		12_500,
		basic.Config{
			PolicyConfig: basic.PolicyConfig{
				ThresholdRatio: &thresholdRatio,
			},
		},
		dataDirectory,
		diagnosticSink,
	)
	defer shutdownCompactionFixtureComposition(t, secondRuntime)
	secondHandle := resumeCompactionFixtureAgent(
		t,
		secondServices,
		"cold-compaction-agent",
	)
	defer disposeCompactionFixtureAgent(t, secondHandle)
	resumedConversation := secondHandle.Subject.SessionValue()
	resumedEntries := resumedConversation.Events()
	if resumedConversation.FirstLiveSeq() != int64(len(firstEntries)) ||
		len(resumedEntries) != len(firstEntries)+1 ||
		resumedEntries[len(resumedEntries)-1].Type != session.EndSeedEventName {
		t.Fatalf(
			"resumed compaction log = firstLive %d, entries %d",
			resumedConversation.FirstLiveSeq(),
			len(resumedEntries),
		)
	}
	resumedState, err := compaction.InspectLog(resumedEntries)
	if err != nil || resumedState.Attempt != nil {
		t.Fatalf("resumed compaction state = %#v, error = %v", resumedState, err)
	}
	if resumedSurface := resumedConversation.Surface(); !reflect.DeepEqual(resumedSurface, firstSurface) {
		t.Fatalf(
			"resumed Surface = %#v, want %#v",
			resumedSurface,
			firstSurface,
		)
	}
	resumedMeasurement, err := secondServices.meter.Measure(
		context.Background(),
		resumedConversation,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resumedMeasurement.LogRevision != int64(len(resumedEntries)) {
		t.Fatalf(
			"resumed Token Meter revision = %d, want %d",
			resumedMeasurement.LogRevision,
			len(resumedEntries),
		)
	}
	firstMeasurement.LogRevision = 0
	resumedMeasurement.LogRevision = 0
	if !reflect.DeepEqual(resumedMeasurement, firstMeasurement) {
		t.Fatalf(
			"resumed Token Meter = %#v, want %#v",
			resumedMeasurement,
			firstMeasurement,
		)
	}
	resumedProjection, err := secondServices.projections.Snapshot(resumedConversation)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resumedProjection.Values, firstProjection.Values) {
		t.Fatalf(
			"resumed Token Meter projections = %#v, want %#v",
			resumedProjection.Values,
			firstProjection.Values,
		)
	}

	sendCompactionFixturePrompt(t, secondHandle.Subject, "continue after restart")
	resumedRequest := waitCompactionFixtureRequestBody(t, requestBodies)
	if !strings.Contains(resumedRequest, "cold restart checkpoint") ||
		strings.Contains(resumedRequest, firstPayload) {
		t.Fatal("cold-resumed request did not reconstruct the checkpoint surface")
	}
	if mainRequests.Load() != 4 || compactRequests.Load() != 1 {
		t.Fatalf(
			"cold restart request counts = main %d, compact %d",
			mainRequests.Load(),
			compactRequests.Load(),
		)
	}
	assertCompactionFixtureTransaction(t, resumedConversation.Events())
}

func startCompactionFixtureComposition(
	testingContext *testing.T,
	baseURL string,
	contextWindow int,
	compactionConfig basic.Config,
	dataDirectory string,
	diagnosticSink *Diagnostics,
) (*plugin.Runtime, *serviceProbe, *Server) {
	testingContext.Helper()
	identityDirectory := testingContext.TempDir()
	directory, err := NewCatalog(Environment{
		WorkingDirectory: testingContext.TempDir(),
		LookupEnv: func(environmentName string) (string, bool) {
			if environmentName == deepseek.DefaultAPIKeyEnv {
				return "compaction-contract-key", true
			}
			return "", false
		},
		UserHomeDir: func() (string, error) {
			return identityDirectory, nil
		},
		Diagnostics: diagnosticSink,
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	specs, err := DefaultSpecs(
		"127.0.0.1:0",
		"compaction-e2e",
		filepath.Join(dataDirectory, "sessions.sqlite"),
		filepath.Join(dataDirectory, "workspaces.sqlite"),
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	modelName := "Compaction Fixture Model"
	models := []deepseek.CatalogModel{
		{
			ID:            deepseek.DefaultModelID,
			Name:          &modelName,
			ContextWindow: &contextWindow,
		},
	}
	deepSeekConfig, err := json.Marshal(deepseek.Config{
		BaseURL: &baseURL,
		Models:  &models,
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	compactionRaw, err := json.Marshal(compactionConfig)
	if err != nil {
		testingContext.Fatal(err)
	}
	titleRaw, err := json.Marshal(title.Config{
		FallbackMaxWords: 5,
		FallbackMaxBytes: 40,
		MaxTitleBytes:    80,
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	for specIndex := range specs {
		switch specs[specIndex].FactoryName {
		case deepseek.PluginName:
			specs[specIndex].Config = deepSeekConfig
		case basic.PluginName:
			specs[specIndex].Config = compactionRaw
		case title.PluginName:
			specs[specIndex].Config = titleRaw
		}
	}
	serviceView, specs := addProbe(testingContext, directory, specs)
	assembledServer, err := BuildServer(context.Background(), directory, specs)
	if err != nil {
		testingContext.Fatal(err)
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: diagnosticSink,
	})
	if _, err = runtimeEngine.Start(context.Background(), assembledServer); err != nil {
		testingContext.Fatal(err)
	}
	resolved, err := serviceView.models.ResolveModelInfo(
		context.Background(),
		deepseek.ProviderRoute,
		deepseek.DefaultModelID,
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	if resolved.Context == nil ||
		resolved.Context.ContextWindow != contextWindow {
		testingContext.Fatalf(
			"resolved fixture model context = %#v, want %d",
			resolved.Context,
			contextWindow,
		)
	}
	return runtimeEngine, serviceView, assembledServer
}

func createCompactionFixtureAgent(
	testingContext *testing.T,
	serviceView *serviceProbe,
	identifier session.SessionID,
) agent.Handle {
	testingContext.Helper()
	handle, err := serviceView.constructor.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: identifier,
			AgentOptions: agent.Options{
				Provider: deepseek.ProviderRoute,
				Model:    deepseek.DefaultModelID,
			},
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return handle
}

func resumeCompactionFixtureAgent(
	testingContext *testing.T,
	serviceView *serviceProbe,
	identifier session.SessionID,
) agent.Handle {
	testingContext.Helper()
	handle, err := serviceView.constructor.Resume(
		context.Background(),
		agent.ResumeOptions{
			SessionID: identifier,
			AgentOptions: agent.Options{
				Provider: deepseek.ProviderRoute,
				Model:    deepseek.DefaultModelID,
			},
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return handle
}

func sendCompactionFixturePrompt(
	testingContext *testing.T,
	subject agent.Agent,
	textValue string,
) {
	testingContext.Helper()
	if err := subject.Followup(compactionFixtureUserMessage(
		testingContext,
		textValue,
	)); err != nil {
		testingContext.Fatal(err)
	}
	waitCompactionFixtureAgentIdle(testingContext, subject)
}

func compactionFixtureUserMessage(
	testingContext *testing.T,
	textValue string,
) agentmessage.UserMessage {
	testingContext.Helper()
	messageValue, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock(textValue),
		},
		Source: agentmessage.UserMessageSource{},
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	return messageValue
}

func waitCompactionFixtureAgentIdle(
	testingContext *testing.T,
	subject agent.Agent,
) {
	testingContext.Helper()
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancelWait()
	if err := subject.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
}

func waitCompactionFixtureRequestBody(
	testingContext *testing.T,
	requestBodies <-chan string,
) string {
	testingContext.Helper()
	select {
	case bodyValue := <-requestBodies:
		return bodyValue
	case <-time.After(5 * time.Second):
		testingContext.Fatal("model request body was not observed")
		return ""
	}
}

func disposeCompactionFixtureAgent(
	testingContext *testing.T,
	handle agent.Handle,
) {
	testingContext.Helper()
	closeContext, cancelClose := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancelClose()
	if err := handle.Dispose(closeContext); err != nil {
		testingContext.Errorf("dispose compaction Agent: %v", err)
	}
}

func shutdownCompactionFixtureComposition(
	testingContext *testing.T,
	runtimeEngine *plugin.Runtime,
) {
	testingContext.Helper()
	closeContext, cancelClose := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancelClose()
	if err := runtimeEngine.Shutdown(closeContext); err != nil {
		testingContext.Errorf("shutdown compaction composition: %v", err)
	}
}

func assertCompactionFixtureTransaction(
	testingContext *testing.T,
	entries []session.Event,
) {
	testingContext.Helper()
	state, err := compaction.InspectLog(entries)
	if err != nil || state.Attempt != nil {
		testingContext.Fatalf("compaction log state = %#v, error = %v", state, err)
	}
	startIndex := -1
	summaryIndex := -1
	endIndex := -1
	for entryIndex, entry := range entries {
		switch entry.Type {
		case compaction.StartEventName:
			startIndex = entryIndex
		case compaction.SummaryEventName:
			summaryIndex = entryIndex
		case compaction.EndEventName:
			endIndex = entryIndex
		}
	}
	if startIndex < 0 || summaryIndex != startIndex+1 ||
		summaryIndex+1 >= len(entries) ||
		entries[summaryIndex+1].Type != session.UserMessageEventName ||
		endIndex != summaryIndex+2 {
		testingContext.Fatalf(
			"compaction transaction positions = start %d, summary %d, end %d",
			startIndex,
			summaryIndex,
			endIndex,
		)
	}
}

func latestCompactionFixtureTurnEndKind(
	testingContext *testing.T,
	entries []session.Event,
) string {
	testingContext.Helper()
	for entryIndex := len(entries) - 1; entryIndex >= 0; entryIndex-- {
		if entries[entryIndex].Type != session.TurnEndEventName {
			continue
		}
		var ending session.TurnEnd
		if err := json.Unmarshal(entries[entryIndex].Data, &ending); err != nil {
			testingContext.Fatal(err)
		}
		return ending.Reason.TurnEndKind()
	}
	testingContext.Fatal("turn/end event is absent")
	return ""
}

func writeCompactionFixtureSSE(
	responseWriter http.ResponseWriter,
	textValue string,
	inputTokens int64,
) {
	responseWriter.Header().Set("content-type", "text/event-stream")
	responseWriter.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(
		responseWriter,
		"data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n",
		textValue,
	)
	_, _ = fmt.Fprintf(
		responseWriter,
		"data: {\"choices\":[{\"delta\":{\"content\":\"\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":4}}\n\n",
		inputTokens,
	)
	_, _ = fmt.Fprint(responseWriter, "data: [DONE]\n\n")
}
