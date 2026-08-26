package subagent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/credentials"
	credentialslocal "github.com/gorenx/goren/credentials/local"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/spawn"
	subagentdelegation "github.com/gorenx/goren/subagent/tools/delegation"
	"github.com/gorenx/goren/tools"
)

const realProviderTestEnvironment = "GOREN_REAL_PROVIDER_TEST"

type realProviderEnvironment struct {
	home string
}

func (environment realProviderEnvironment) Lookup(variableName string) (string, bool) {
	return os.LookupEnv(variableName)
}

func (environment realProviderEnvironment) UserHomeDir() (string, error) {
	return environment.home, nil
}

func TestRealProviderForegroundOneShot(t *testing.T) {
	if os.Getenv(realProviderTestEnvironment) != "1" {
		t.Skip("set GOREN_REAL_PROVIDER_TEST=1 to run the real-provider acceptance test")
	}
	if _, configured := os.LookupEnv(deepseek.DefaultAPIKeyEnv); !configured {
		t.Skip("DeepSeek credential is not present in the process environment")
	}
	environment := realProviderEnvironment{
		home: t.TempDir(),
	}
	credentialStore, credentialErr := credentialslocal.NewLiveStore(
		credentialslocal.Config{
			Path: filepath.Join(t.TempDir(), ".credentials.json"),
		},
	)
	if credentialErr != nil {
		t.Fatal(credentialErr)
	}
	credentialManager, credentialErr := credentials.NewManager(
		credentialStore,
		environment,
	)
	if credentialErr != nil {
		t.Fatal(credentialErr)
	}
	providerFactory, providerErr := deepseek.NewFactory(environment)
	if providerErr != nil {
		t.Fatal(providerErr)
	}
	providerConfig := deepseek.Config{}
	if baseURL := os.Getenv("DEEPSEEK_API_BASE_URL"); baseURL != "" {
		providerConfig.BaseURL = &baseURL
	}
	rawProviderConfig, encodeErr := json.Marshal(providerConfig)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	providerPlugin, providerErr := providerFactory.Create(
		context.Background(),
		rawProviderConfig,
	)
	if providerErr != nil {
		t.Fatal(providerErr)
	}
	maxTokens := 64
	state := newIntegrationFixtureWithConfiguration(
		t,
		integrationConfiguration{
			agentOptions: agent.Options{
				Provider:  deepseek.ProviderRoute,
				Model:     deepseek.DefaultModelID,
				MaxTokens: &maxTokens,
			},
			plugins: []plugin.Plugin{
				credentialManager,
				providerPlugin,
			},
			delegation: subagentdelegation.Settings{
				SeedBuilder:           spawn.DefaultSeedBuilderName,
				ToolName:              subagentdelegation.DefaultToolName,
				EnableRunInBackground: false,
				BackgroundMode:        subagentdelegation.BackgroundOneShot,
			},
		},
	)
	parentHandle := state.createParent(t)
	requestContext, cancelRequest := context.WithTimeout(
		context.Background(),
		90*time.Second,
	)
	defer cancelRequest()
	outcome := state.toolRuntime.Execute(
		requestContext,
		tools.ToolExecutionInput{
			CallID:     "real-delegate-1",
			RootCallID: "real-delegate-1",
			Name:       subagentdelegation.DefaultToolName,
			Arguments: json.RawMessage(`{
  "description": "verify real provider",
  "prompt": "Reply with exactly GOREN_SUBAGENT_OK and no other text."
}`),
			Subject: parentHandle.Subject,
		},
	)
	if outcome.Failed() {
		failure, _ := outcome.FailureDetail()
		t.Fatalf("real-provider delegation failed: %s", failure.Message)
	}
	if !strings.Contains(visibleResultText(outcome), "GOREN_SUBAGENT_OK") {
		t.Fatalf("real-provider output did not contain the acceptance marker")
	}
	if eventErr := state.lifecycle.waitForEnd(requestContext); eventErr != nil {
		t.Fatal(eventErr)
	}
	starts, ends := state.lifecycle.snapshot()
	if len(starts) != 1 || len(ends) != 1 ||
		ends[0].StopReason != subagent.StopCompleted {
		t.Fatalf("real-provider lifecycle counts/reason = %d/%d/%q", len(starts), len(ends), ends[0].StopReason)
	}
	if liveAgents := state.agents.List(); len(liveAgents) != 1 ||
		liveAgents[0] != parentHandle.Subject {
		t.Fatalf("real-provider one-shot left %d live Agents", len(liveAgents))
	}
	if failures := state.eventFailures.snapshot(); len(failures) != 0 {
		t.Fatalf("real-provider event observer failures = %d", len(failures))
	}
}

func visibleResultText(outcome tools.ToolExecutionResult) string {
	var result strings.Builder
	for _, block := range outcome.ContentBlocks() {
		plain, matches := block.(llm.PlainTextContent)
		if !matches {
			continue
		}
		textValue, visible := plain.PlainText()
		if visible {
			result.WriteString(textValue)
		}
	}
	return result.String()
}
