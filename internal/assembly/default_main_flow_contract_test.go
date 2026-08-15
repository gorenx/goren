//go:build contract

package assembly

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/internal/llmdeepseek"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	contractfixture "github.com/gorenx/goren/tests/contract/fixture"
)

type defaultMainFlowObservation struct {
	SessionID string `json:"sessionId"`
	Model     struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	} `json:"model"`
	Routable     bool     `json:"routable"`
	Accepted     bool     `json:"accepted"`
	Idle         bool     `json:"idle"`
	HistoryTypes []string `json:"historyTypes"`
}

type defaultMainFlowDeepSeekRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role string `json:"role"`
	} `json:"messages"`
}

func TestDefaultCompositionServesFixedTypeScriptClientThroughDeepSeekAdapter(t *testing.T) {
	var requestCount atomic.Int32
	deepSeekServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		defer httpRequest.Body.Close()
		var received defaultMainFlowDeepSeekRequest
		if err := json.NewDecoder(httpRequest.Body).Decode(&received); err != nil {
			http.Error(responseWriter, "invalid request", http.StatusBadRequest)
			return
		}
		if httpRequest.Header.Get("authorization") != "Bearer contract-key" ||
			received.Model != "deepseek-v4-flash" || !received.Stream || len(received.Messages) == 0 {
			http.Error(responseWriter, "unexpected request", http.StatusBadRequest)
			return
		}
		requestCount.Add(1)
		responseWriter.Header().Set("content-type", "text/event-stream")
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(responseWriter, "data: {\"choices\":[{\"delta\":{\"content\":\"hello from DeepSeek\"}}]}\n\n")
		_, _ = fmt.Fprint(responseWriter, "data: {\"choices\":[{\"delta\":{\"content\":\"\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4}}\n\n")
		_, _ = fmt.Fprint(responseWriter, "data: [DONE]\n\n")
	}))
	defer deepSeekServer.Close()

	repositoryRoot, sourceRoot := contractfixture.Paths(t)
	workingDirectory := t.TempDir()
	dataDirectory := t.TempDir()
	identityDirectory := t.TempDir()
	factoryCatalog, err := NewCatalog(Environment{
		WorkingDirectory: workingDirectory,
		LookupEnv: func(environmentName string) (string, bool) {
			if environmentName == llmdeepseek.DefaultAPIKeyEnv {
				return "contract-key", true
			}
			return "", false
		},
		UserHomeDir: func() (string, error) { return identityDirectory, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	compositionSpecs, err := DefaultSpecs(
		"127.0.0.1:0",
		"contract",
		filepath.Join(dataDirectory, "sessions.sqlite"),
		filepath.Join(dataDirectory, "workspaces.sqlite"),
	)
	if err != nil {
		t.Fatal(err)
	}
	baseURL := deepSeekServer.URL
	deepSeekConfig, err := json.Marshal(llmdeepseek.Config{BaseURL: &baseURL})
	if err != nil {
		t.Fatal(err)
	}
	for specIndex := range compositionSpecs {
		if compositionSpecs[specIndex].FactoryName == DeepSeekFactoryName {
			compositionSpecs[specIndex].Config = deepSeekConfig
		}
	}

	runtimeEngine := plugin.NewRuntime()
	if _, err := Load(context.Background(), runtimeEngine, factoryCatalog, compositionSpecs); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeContext, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelClose()
		if err := runtimeEngine.Shutdown(closeContext); err != nil {
			t.Errorf("shutdown default composition: %v", err)
		}
	})

	serverAddress := ""
	serverProbe := probePlugin{body: func(_ context.Context, pluginScope *plugin.Scope) error {
		serverEndpoint, found := plugin.Require(pluginScope, serverServiceKey)
		if !found {
			return fmt.Errorf("webServer service is unavailable")
		}
		serverAddress = serverEndpoint.Address()
		return nil
	}}
	if _, err := runtimeEngine.Load(context.Background(), serverProbe); err != nil {
		t.Fatal(err)
	}

	commandContext, cancelCommand := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelCommand()
	output, err := contractfixture.RunTypeScript(
		commandContext,
		sourceRoot,
		nil,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "default-main-flow-client.ts"),
		sourceRoot,
		"http://"+serverAddress,
		workingDirectory,
	)
	if err != nil {
		t.Fatalf("fixed TypeScript main flow: %v", err)
	}
	var observation defaultMainFlowObservation
	if err := json.Unmarshal(output, &observation); err != nil {
		t.Fatalf("decode fixed TypeScript observation: %v; output = %s", err, output)
	}
	if observation.SessionID != "default-main-flow-contract" ||
		observation.Model.Provider != llmdeepseek.ProviderRoute ||
		observation.Model.Model != "deepseek-v4-flash" ||
		!observation.Routable || !observation.Accepted || !observation.Idle {
		t.Fatalf("main flow observation = %#v", observation)
	}
	for _, eventName := range []string{
		session.UserMessageEventName,
		session.AssistantMessageEventName,
		session.TurnEndEventName,
	} {
		if !slices.Contains(observation.HistoryTypes, eventName) {
			t.Fatalf("history types = %v, missing %s", observation.HistoryTypes, eventName)
		}
	}
	if requestCount.Load() != 1 {
		t.Fatalf("DeepSeek request count = %d, want 1", requestCount.Load())
	}
}
