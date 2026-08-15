//go:build contract

package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gorenx/goren/apiproxy"
	connectionhost "github.com/gorenx/goren/internal/connection"
	"github.com/gorenx/goren/llm"
)

type llmAPIContractDirectory struct {
	declared bool
}

func (*llmAPIContractDirectory) ListProviders() []llm.ProviderInfo {
	return []llm.ProviderInfo{{ID: "deepseek-official", Name: "DeepSeek"}, {ID: "private", Name: "Private"}}
}

func (fixture *llmAPIContractDirectory) ListConfigurableProviders() []llm.ConfigurableProvider {
	return []llm.ConfigurableProvider{
		{
			Provider: "deepseek-official", DisplayName: "DeepSeek", SettingsNS: "llm-deepseek",
			SettingsPath: []string{},
		},
		{
			Provider: "dormant", DisplayName: "Dormant", SettingsNS: "llm-custom",
			SettingsPath: []string{"providers", "dormant"}, Declared: &fixture.declared,
		},
	}
}

func (*llmAPIContractDirectory) ListModels(_ context.Context, providerRoute string) ([]llm.ModelInfo, error) {
	switch providerRoute {
	case "deepseek-official":
		return []llm.ModelInfo{{
			Provider: providerRoute, ID: "deepseek-v4-flash", Name: "DeepSeek-V4-Flash",
			Description: "Fast model",
		}}, nil
	case "private":
		return nil, errors.New("private catalog unavailable")
	default:
		return []llm.ModelInfo{}, nil
	}
}

func (*llmAPIContractDirectory) ResolveModelInfo(
	_ context.Context,
	providerRoute string,
	modelID string,
) (llm.ResolvedModelInfo, error) {
	return llm.ResolvedModelInfo{
		ModelInfo: llm.ModelInfo{Provider: providerRoute, ID: modelID, Name: "DeepSeek-V4-Flash"},
		Reasoning: &llm.ModelReasoningInfo{
			Efforts: []llm.ReasoningEffortInfo{
				{ID: "off", Name: "Off"},
				{ID: "high", Name: "High", Description: "More reasoning"},
			},
			DefaultEffort: "high",
		},
	}, nil
}

type llmAPIClientObservation struct {
	Providers apiproxy.LLMProvidersValue `json:"providers"`
	Models    apiproxy.LLMModelsValue    `json:"models"`
}

func TestPinnedSourceLLMWebApiClientTalksToGoGateway(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	directory := &llmAPIContractDirectory{}
	gateway, err := apiproxy.NewLLMGateway(directory)
	if err != nil {
		t.Fatal(err)
	}
	methods := apiproxy.NewCatalog()
	if err := apiproxy.RegisterLLMAPI(methods, gateway); err != nil {
		t.Fatal(err)
	}
	idleMux := func(requestContext context.Context, _ func(apiproxy.StreamRequest[apiproxy.MuxFrame]) error) error {
		<-requestContext.Done()
		return nil
	}
	idleHost := func(requestContext context.Context, _ func(apiproxy.StreamRequest[apiproxy.HostFrame]) error) error {
		<-requestContext.Done()
		return nil
	}
	streams, err := apiproxy.NewEventStreams(idleMux, idleHost)
	if err != nil {
		t.Fatal(err)
	}
	httpHost, err := connectionhost.NewHTTPHost(connectionhost.HTTPConfig{}, methods, streams)
	if err != nil {
		t.Fatal(err)
	}
	testServer := httptest.NewServer(httpHost)
	defer testServer.Close()
	defer func() {
		closeContext, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelClose()
		if err := httpHost.Close(closeContext); err != nil {
			t.Errorf("close Go host: %v", err)
		}
	}()

	commandContext, cancelCommand := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCommand()
	output, err := runTypeScript(commandContext, sourceRoot,
		filepath.Join(repositoryRoot, "tests", "contract", "typescript", "llm-client.ts"),
		sourceRoot, testServer.URL,
	)
	if err != nil {
		t.Fatal(err)
	}
	var observation llmAPIClientObservation
	if err := json.Unmarshal(output, &observation); err != nil {
		t.Fatalf("decode source LLM client observation: %v; output = %s", err, output)
	}

	declared := false
	wantProviders := apiproxy.LLMProvidersValue{Providers: []apiproxy.ConfigurableProviderView{
		{
			Provider: "deepseek-official", DisplayName: "DeepSeek", SettingsNS: "llm-deepseek",
			SettingsPath: []string{}, Active: true,
		},
		{
			Provider: "dormant", DisplayName: "Dormant", SettingsNS: "llm-custom",
			SettingsPath: []string{"providers", "dormant"}, Active: false, Declared: &declared,
		},
		{Provider: "private", DisplayName: "Private", SettingsNS: "", SettingsPath: []string{}, Active: true},
	}}
	wantModels := apiproxy.LLMModelsValue{
		Groups: []apiproxy.ModelProviderGroup{{
			ID: "deepseek-official", Name: "DeepSeek",
			Models: []apiproxy.ModelCatalogModel{{
				ID: "deepseek-v4-flash", Name: "DeepSeek-V4-Flash", Description: "Fast model",
				Reasoning: &apiproxy.ModelReasoning{
					Efforts: []apiproxy.ModelReasoningEffort{
						{ID: "off", Name: "Off"},
						{ID: "high", Name: "High", Description: "More reasoning"},
					},
					DefaultEffort: "high",
				},
			}},
		}},
		Failures: []apiproxy.ModelCatalogFailure{{
			ID: "private", Name: "Private", Message: "private catalog unavailable",
		}},
	}
	if !reflect.DeepEqual(observation.Providers, wantProviders) {
		t.Fatalf("providers = %#v, want %#v", observation.Providers, wantProviders)
	}
	if !reflect.DeepEqual(observation.Models, wantModels) {
		t.Fatalf("models = %#v, want %#v", observation.Models, wantModels)
	}
}
