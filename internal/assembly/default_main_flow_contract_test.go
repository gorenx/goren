//go:build contract

package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
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

type webUIMainFlowObservation struct {
	Booted               bool `json:"booted"`
	Prompted             bool `json:"prompted"`
	Selected             bool `json:"selected"`
	History              bool `json:"history"`
	QuestionAnswered     bool `json:"questionAnswered"`
	RuntimeContextHidden bool `json:"runtimeContextHidden"`
	Localized            bool `json:"localized"`
}

type defaultMainFlowDeepSeekRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Thinking *struct {
		Type string `json:"type"`
	} `json:"thinking,omitempty"`
	MaxTokens *int `json:"max_tokens,omitempty"`
	Messages  []struct {
		Role string `json:"role"`
	} `json:"messages"`
}

func TestDefaultCompositionServesFixedTypeScriptClientThroughDeepSeekAdapter(t *testing.T) {
	var mainRequestCount atomic.Int32
	var titleRequestCount atomic.Int32
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
		if received.Thinking != nil && received.Thinking.Type == "disabled" &&
			received.MaxTokens != nil && *received.MaxTokens == 64 {
			titleRequestCount.Add(1)
			responseWriter.Header().Set("content-type", "text/event-stream")
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(responseWriter, "data: {\"choices\":[{\"delta\":{\"content\":\"Generated session title\"}}]}\n\n")
			_, _ = fmt.Fprint(responseWriter, "data: {\"choices\":[{\"delta\":{\"content\":\"\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4}}\n\n")
			_, _ = fmt.Fprint(responseWriter, "data: [DONE]\n\n")
			return
		}
		requestNumber := mainRequestCount.Add(1)
		if requestNumber == 3 {
			arguments := `{"questions":[{"id":"focus","question":"你想让我重点评价什么？","header":"选择方向","options":[{"label":"架构","description":"关注模块边界和依赖方向。"},{"label":"代码","description":"关注实现质量和测试。"}]}]}`
			responseWriter.Header().Set("content-type", "text/event-stream")
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(responseWriter, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-question\",\"function\":{\"name\":\"ask_user_question\",\"arguments\":%q}}]}}]}\n\n", arguments)
			_, _ = fmt.Fprint(responseWriter, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":6}}\n\n")
			_, _ = fmt.Fprint(responseWriter, "data: [DONE]\n\n")
			return
		}
		responseText := "hello from DeepSeek"
		if requestNumber == 2 {
			responseText = "hello from the Web UI"
		} else if requestNumber == 4 {
			hasToolMessage := false
			for _, receivedMessage := range received.Messages {
				if receivedMessage.Role == "tool" {
					hasToolMessage = true
					break
				}
			}
			if !hasToolMessage {
				http.Error(responseWriter, "question answer did not reach the continuation request", http.StatusBadRequest)
				return
			}
			responseText = "question answered through the Web UI"
		}
		responseWriter.Header().Set("content-type", "text/event-stream")
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(responseWriter, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", responseText)
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
	pageResponse, err := http.Get("http://" + serverAddress + "/")
	if err != nil {
		t.Fatal(err)
	}
	pageBody, readErr := io.ReadAll(pageResponse.Body)
	closeErr := pageResponse.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatal(fmt.Errorf("read Web shell: %w", errors.Join(readErr, closeErr)))
	}
	if pageResponse.StatusCode != http.StatusOK ||
		!strings.Contains(string(pageBody), `id="root"`) ||
		!strings.Contains(string(pageBody), "/assets/app-") {
		t.Fatalf("Web shell response = (%d, %q)", pageResponse.StatusCode, pageBody)
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

	webOutput, err := contractfixture.RunTypeScript(
		commandContext,
		sourceRoot,
		nil,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "web-ui-main-flow.ts"),
		sourceRoot,
		"http://"+serverAddress,
		"hello from the Web UI",
	)
	if err != nil {
		t.Fatalf("embedded Web UI main flow after %d main DeepSeek requests: %v", mainRequestCount.Load(), err)
	}
	var webObservation webUIMainFlowObservation
	if err := json.Unmarshal(webOutput, &webObservation); err != nil {
		t.Fatalf("decode Web UI observation: %v; output = %s", err, webOutput)
	}
	if !webObservation.Booted || !webObservation.Prompted || !webObservation.Selected || !webObservation.History ||
		!webObservation.QuestionAnswered || !webObservation.RuntimeContextHidden || !webObservation.Localized {
		t.Fatalf("Web UI observation = %#v", webObservation)
	}
	if mainRequestCount.Load() != 4 || titleRequestCount.Load() != 1 {
		t.Fatalf(
			"DeepSeek request counts = main %d title %d, want main 4 title 1",
			mainRequestCount.Load(), titleRequestCount.Load(),
		)
	}
}
