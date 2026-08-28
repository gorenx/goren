package assembly

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/title"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

func TestDefaultCompositionRestoresBoundDeliveryAcrossSQLiteRestart(
	testingContext *testing.T,
) {
	var requestCount atomic.Int32
	requestBodies := make(chan string, 8)
	providerServer := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
			defer httpRequest.Body.Close()
			bodyValue, err := io.ReadAll(httpRequest.Body)
			if err != nil {
				http.Error(responseWriter, err.Error(), http.StatusBadRequest)
				return
			}
			requestBodies <- string(bodyValue)
			requestNumber := requestCount.Add(1)
			writeCompactionFixtureSSE(
				responseWriter,
				fmt.Sprintf("bound restart response %d", requestNumber),
				100,
			)
		},
	))
	defer providerServer.Close()

	dataDirectory := testingContext.TempDir()
	workingDirectory := testingContext.TempDir()
	failureReporting := testDiagnostics(testingContext)
	firstRuntime, firstServices, firstServer := startBoundRestartComposition(
		testingContext,
		providerServer.URL,
		dataDirectory,
		workingDirectory,
		failureReporting,
	)
	firstRuntimeOpen := true
	defer func() {
		if firstRuntimeOpen {
			shutdownBoundRestartComposition(testingContext, firstRuntime)
		}
	}()
	createdResult := callSessionUnary(
		testingContext,
		firstServer.BoundAddress(),
		apiproxy.BoundCreateMethod,
		"bound-create-before-restart",
		`{"definition":{"name":"researcher","enabled":true,"systemPrompt":"Bound restart system prompt","extensions":[]}}`,
	)
	var created apiproxy.BoundDefinitionValue
	if err := json.Unmarshal(createdResult.Value, &created); err != nil {
		testingContext.Fatal(err)
	}
	if created.Definition.Name != "researcher" ||
		created.Definition.Revision != 1 {
		testingContext.Fatalf("created Bound Definition = %#v", created)
	}
	firstParent := createBoundRestartParent(
		testingContext,
		firstServices,
		"bound-restart-parent",
		workingDirectory,
	)
	firstParentOpen := true
	defer func() {
		if firstParentOpen {
			disposeBoundRestartAgent(testingContext, firstParent)
		}
	}()
	firstBinding, firstChild := waitForAssemblyBoundChild(
		testingContext,
		firstServices,
		firstParent.Subject,
		"researcher",
		1,
	)
	if observed := requestCount.Load(); observed != 0 {
		testingContext.Fatalf(
			"Bound restore fixture materialization issued %d model requests",
			observed,
		)
	}
	sendAssemblyBoundPrompt(
		testingContext,
		firstParent.Subject,
		"first parent interaction before restart",
	)
	firstParentRequest := waitAssemblyBoundRequest(
		testingContext,
		requestBodies,
	)
	firstChildRequest := waitAssemblyBoundRequest(
		testingContext,
		requestBodies,
	)
	waitAssemblyBoundIdle(testingContext, firstChild)
	if !strings.Contains(
		firstParentRequest,
		"first parent interaction before restart",
	) || !strings.Contains(firstChildRequest, "Bound restart system prompt") ||
		!strings.Contains(
			firstChildRequest,
			"first parent interaction before restart",
		) {
		testingContext.Fatalf(
			"first Bound request pair = %q / %q",
			firstParentRequest,
			firstChildRequest,
		)
	}
	assertAssemblyBoundDeliveryCount(
		testingContext,
		firstChild.SessionValue(),
		1,
	)
	if err := firstServices.sessions.Flush(
		context.Background(),
		firstParent.Subject.SessionValue(),
	); err != nil {
		testingContext.Fatal(err)
	}
	if err := firstServices.sessions.Flush(
		context.Background(),
		firstChild.SessionValue(),
	); err != nil {
		testingContext.Fatal(err)
	}
	disposeBoundRestartAgent(testingContext, firstParent)
	firstParentOpen = false
	shutdownBoundRestartComposition(testingContext, firstRuntime)
	firstRuntimeOpen = false

	secondRuntime, secondServices, _ := startBoundRestartComposition(
		testingContext,
		providerServer.URL,
		dataDirectory,
		workingDirectory,
		failureReporting,
	)
	defer shutdownBoundRestartComposition(testingContext, secondRuntime)
	secondParent := resumeBoundRestartParent(
		testingContext,
		secondServices,
		"bound-restart-parent",
	)
	defer disposeBoundRestartAgent(testingContext, secondParent)
	secondBinding, secondChild := waitForAssemblyBoundChild(
		testingContext,
		secondServices,
		secondParent.Subject,
		"researcher",
		1,
	)
	if secondBinding.ChildSessionID != firstBinding.ChildSessionID {
		testingContext.Fatalf(
			"Bound child identity changed across restart: %q -> %q",
			firstBinding.ChildSessionID,
			secondBinding.ChildSessionID,
		)
	}
	if observed := requestCount.Load(); observed != 2 {
		testingContext.Fatalf(
			"Bound cold restore changed model count to %d, want 2",
			observed,
		)
	}
	sendAssemblyBoundPrompt(
		testingContext,
		secondParent.Subject,
		"second parent interaction after restart",
	)
	secondParentRequest := waitAssemblyBoundRequest(
		testingContext,
		requestBodies,
	)
	secondChildRequest := waitAssemblyBoundRequest(
		testingContext,
		requestBodies,
	)
	waitAssemblyBoundIdle(testingContext, secondChild)
	if !strings.Contains(
		secondParentRequest,
		"second parent interaction after restart",
	) || !strings.Contains(secondChildRequest, "Bound restart system prompt") ||
		!strings.Contains(
			secondChildRequest,
			"second parent interaction after restart",
		) {
		testingContext.Fatalf(
			"second Bound request pair = %q / %q",
			secondParentRequest,
			secondChildRequest,
		)
	}
	assertAssemblyBoundDeliveryCount(
		testingContext,
		secondChild.SessionValue(),
		2,
	)
	if observed := requestCount.Load(); observed != 4 {
		testingContext.Fatalf("model request count = %d, want 4", observed)
	}
}

func startBoundRestartComposition(
	testingContext *testing.T,
	baseURL string,
	dataDirectory string,
	workingDirectory string,
	failureReporting *Diagnostics,
) (*plugin.Runtime, *serviceProbe, *Server) {
	testingContext.Helper()
	identityDirectory := testingContext.TempDir()
	directory, err := NewCatalog(Environment{
		WorkingDirectory: workingDirectory,
		LookupEnv: func(environmentName string) (string, bool) {
			if environmentName == deepseek.DefaultAPIKeyEnv {
				return "bound-restart-contract-key", true
			}
			return "", false
		},
		UserHomeDir: func() (string, error) {
			return identityDirectory, nil
		},
		Diagnostics: failureReporting,
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	specs, err := DefaultSpecs(
		"127.0.0.1:0",
		"bound-restart-e2e",
		filepath.Join(dataDirectory, "sessions.sqlite"),
		filepath.Join(dataDirectory, "workspaces.sqlite"),
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	deepSeekConfig, err := json.Marshal(deepseek.Config{
		BaseURL: &baseURL,
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	titleConfig, err := json.Marshal(title.Config{
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
		case title.PluginName:
			specs[specIndex].Config = titleConfig
		}
	}
	services, specs := addProbe(testingContext, directory, specs)
	assembledServer, err := BuildServer(
		context.Background(),
		directory,
		specs,
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: failureReporting,
	})
	if _, err = runtimeEngine.Start(
		context.Background(),
		assembledServer,
	); err != nil {
		testingContext.Fatal(err)
	}
	return runtimeEngine, services, assembledServer
}

func createBoundRestartParent(
	testingContext *testing.T,
	services *serviceProbe,
	identifier session.SessionID,
	workingDirectory string,
) agent.Handle {
	testingContext.Helper()
	handle, err := services.constructor.Create(
		context.Background(),
		agent.CreateOptions{
			SessionID: identifier,
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
		testingContext.Fatal(err)
	}
	return handle
}

func resumeBoundRestartParent(
	testingContext *testing.T,
	services *serviceProbe,
	identifier session.SessionID,
) agent.Handle {
	testingContext.Helper()
	handle, err := services.constructor.Resume(
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

func waitForAssemblyBoundChild(
	testingContext *testing.T,
	services *serviceProbe,
	parentAgent agent.Agent,
	boundName string,
	wantRevision int64,
) (boundcontract.BindingData, agent.Agent) {
	testingContext.Helper()
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancelWait()
	changed := time.NewTicker(time.Millisecond)
	defer changed.Stop()
	for {
		bindingValue, found := assemblyBoundBinding(
			testingContext,
			parentAgent.SessionValue().Events(),
			boundName,
		)
		if found {
			childAgent, live := services.agents.Get(bindingValue.ChildSessionID)
			if live && assemblyBoundAppliedRevision(
				testingContext,
				childAgent.SessionValue().Events(),
				wantRevision,
			) {
				return bindingValue, childAgent
			}
		}
		select {
		case <-waitContext.Done():
			testingContext.Fatalf(
				"Bound %q did not materialize after composition start: %v",
				boundName,
				context.Cause(waitContext),
			)
		case <-changed.C:
		}
	}
}

func assemblyBoundBinding(
	testingContext *testing.T,
	events []session.Event,
	boundName string,
) (boundcontract.BindingData, bool) {
	testingContext.Helper()
	for _, committed := range events {
		if committed.Type != boundcontract.BindingEventName {
			continue
		}
		var bindingValue boundcontract.BindingData
		if err := json.Unmarshal(committed.Data, &bindingValue); err != nil {
			testingContext.Fatal(err)
		}
		if bindingValue.Name == boundName {
			return bindingValue, true
		}
	}
	return boundcontract.BindingData{}, false
}

func assemblyBoundAppliedRevision(
	testingContext *testing.T,
	events []session.Event,
	wantRevision int64,
) bool {
	testingContext.Helper()
	for _, committed := range events {
		if committed.Type != boundcontract.DefinitionAppliedEventName {
			continue
		}
		var applied boundcontract.DefinitionAppliedData
		if err := json.Unmarshal(committed.Data, &applied); err != nil {
			testingContext.Fatal(err)
		}
		if applied.Version == boundcontract.EventVersion &&
			applied.Definition.Revision == wantRevision {
			return true
		}
	}
	return false
}

func sendAssemblyBoundPrompt(
	testingContext *testing.T,
	subject agent.Agent,
	textValue string,
) {
	testingContext.Helper()
	messageValue, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock(textValue),
			},
			Source: agentmessage.UserMessageSource{},
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	if err = subject.Followup(messageValue); err != nil {
		testingContext.Fatal(err)
	}
	waitAssemblyBoundIdle(testingContext, subject)
}

func waitAssemblyBoundIdle(
	testingContext *testing.T,
	subject agent.Agent,
) {
	testingContext.Helper()
	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancelWait()
	if err := subject.WhenIdle(waitContext); err != nil {
		testingContext.Fatal(err)
	}
}

func waitAssemblyBoundRequest(
	testingContext *testing.T,
	requestBodies <-chan string,
) string {
	testingContext.Helper()
	select {
	case bodyValue := <-requestBodies:
		return bodyValue
	case <-time.After(5 * time.Second):
		testingContext.Fatal("timed out waiting for Bound provider request")
		return ""
	}
}

func assertAssemblyBoundDeliveryCount(
	testingContext *testing.T,
	conversation session.Context,
	want int,
) {
	testingContext.Helper()
	messages, err := conversation.DeriveMessages()
	if err != nil {
		testingContext.Fatal(err)
	}
	deliveries := 0
	for _, messageValue := range messages {
		origin := messageValue.SourceValue()
		if origin != nil && origin.SourceKind() == boundcontract.DeliveryKind {
			deliveries++
		}
	}
	if deliveries != want {
		testingContext.Fatalf(
			"durable Bound delivery count = %d, want %d",
			deliveries,
			want,
		)
	}
}

func disposeBoundRestartAgent(
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
		testingContext.Fatal(err)
	}
}

func shutdownBoundRestartComposition(
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
		testingContext.Fatal(err)
	}
}
