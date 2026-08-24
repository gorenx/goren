package assembly

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/compaction/basic"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

const compactionRealProviderTestEnvironment = "GOREN_REAL_PROVIDER_TEST"

func TestDefaultCompositionStartsCompactionWithoutCredential(t *testing.T) {
	dataDirectory := t.TempDir()
	diagnosticSink := testDiagnostics(t)
	directory, err := NewCatalog(Environment{
		WorkingDirectory: t.TempDir(),
		LookupEnv: func(string) (string, bool) {
			return "", false
		},
		UserHomeDir: func() (string, error) {
			return t.TempDir(), nil
		},
		Diagnostics: diagnosticSink,
	})
	if err != nil {
		t.Fatal(err)
	}
	specs, err := DefaultSpecs(
		"127.0.0.1:0",
		"compaction-keyless",
		filepath.Join(dataDirectory, "sessions.sqlite"),
		filepath.Join(dataDirectory, "workspaces.sqlite"),
	)
	if err != nil {
		t.Fatal(err)
	}
	serviceView, specs := addProbe(t, directory, specs)
	assembledServer, err := BuildServer(context.Background(), directory, specs)
	if err != nil {
		t.Fatal(err)
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: diagnosticSink,
	})
	if _, err = runtimeEngine.Start(context.Background(), assembledServer); err != nil {
		t.Fatal(err)
	}
	defer shutdownCompactionFixtureComposition(t, runtimeEngine)
	if serviceView.compactor == nil || serviceView.meter == nil ||
		serviceView.pruner == nil {
		t.Fatal("keyless default composition did not publish the Compaction stack")
	}
}

func TestRealProviderCompactsOneRegion(t *testing.T) {
	if os.Getenv(compactionRealProviderTestEnvironment) != "1" {
		t.Skip("set GOREN_REAL_PROVIDER_TEST=1 to run real-provider acceptance")
	}
	if _, configured := os.LookupEnv(deepseek.DefaultAPIKeyEnv); !configured {
		t.Skip("DeepSeek credential is not present in the process environment")
	}

	dataDirectory := t.TempDir()
	diagnosticSink := testDiagnostics(t)
	directory, err := NewCatalog(Environment{
		WorkingDirectory: t.TempDir(),
		LookupEnv:        os.LookupEnv,
		UserHomeDir: func() (string, error) {
			return t.TempDir(), nil
		},
		Diagnostics: diagnosticSink,
	})
	if err != nil {
		t.Fatal(err)
	}
	specs, err := DefaultSpecs(
		"127.0.0.1:0",
		"compaction-real-provider",
		filepath.Join(dataDirectory, "sessions.sqlite"),
		filepath.Join(dataDirectory, "workspaces.sqlite"),
	)
	if err != nil {
		t.Fatal(err)
	}
	automatic := false
	maximum := 1024
	compactionRaw, err := json.Marshal(basic.Config{
		PolicyConfig: basic.PolicyConfig{
			MaxTokens: &maximum,
		},
		Auto: &automatic,
	})
	if err != nil {
		t.Fatal(err)
	}
	providerSettings := deepseek.Config{}
	if baseURL := os.Getenv("DEEPSEEK_API_BASE_URL"); baseURL != "" {
		providerSettings.BaseURL = &baseURL
	}
	providerRaw, err := json.Marshal(providerSettings)
	if err != nil {
		t.Fatal(err)
	}
	for specIndex := range specs {
		switch specs[specIndex].FactoryName {
		case basic.PluginName:
			specs[specIndex].Config = compactionRaw
		case deepseek.PluginName:
			specs[specIndex].Config = providerRaw
		}
	}
	serviceView, specs := addProbe(t, directory, specs)
	assembledServer, err := BuildServer(context.Background(), directory, specs)
	if err != nil {
		t.Fatal(err)
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: diagnosticSink,
	})
	if _, err = runtimeEngine.Start(context.Background(), assembledServer); err != nil {
		t.Fatal(err)
	}
	defer shutdownCompactionFixtureComposition(t, runtimeEngine)
	handle := createCompactionFixtureAgent(t, serviceView, "real-compaction-agent")
	defer disposeCompactionFixtureAgent(t, handle)
	conversation := handle.Subject.SessionValue()
	if _, err = session.Append(
		conversation,
		session.TurnStarted,
		session.TurnStart{
			Turn: 1,
		},
	); err != nil {
		t.Fatal(err)
	}
	firstMessage := compactionFixtureUserMessage(
		t,
		strings.Repeat("first durable real-provider history ", 1200),
	)
	firstEvent, err := session.AppendSurface(
		conversation,
		session.UserMessageAdded,
		firstMessage,
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	secondMessage := compactionFixtureUserMessage(
		t,
		strings.Repeat("second durable real-provider history ", 1200),
	)
	secondEvent, err := session.AppendSurface(
		conversation,
		session.UserMessageAdded,
		secondMessage,
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancelRequest := context.WithTimeout(
		context.Background(),
		120*time.Second,
	)
	defer cancelRequest()
	outcome, err := serviceView.compactor.CompactRegion(
		requestContext,
		firstEvent.Seq,
		secondEvent.Seq,
		compaction.AgentContext{
			Session:  conversation,
			Provider: deepseek.ProviderRoute,
			Model:    deepseek.DefaultModelID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Summary) == 0 ||
		conversation.Surface().ReplaceGeneration != 1 {
		t.Fatalf("real-provider Compaction outcome = %#v", outcome)
	}
	state, err := compaction.InspectLog(conversation.Events())
	if err != nil || state.Attempt != nil {
		t.Fatalf("real-provider Compaction state = %#v, error = %v", state, err)
	}
}
