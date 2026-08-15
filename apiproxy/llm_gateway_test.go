package apiproxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm"
)

type llmDirectoryFixture struct {
	routes       []llm.ProviderInfo
	declarations []llm.ConfigurableProvider
	catalogs     map[string][]llm.ModelInfo
	resolved     map[string]llm.ResolvedModelInfo
	failures     map[string]error
}

func (fixture *llmDirectoryFixture) ListProviders() []llm.ProviderInfo {
	return append([]llm.ProviderInfo{}, fixture.routes...)
}

func (fixture *llmDirectoryFixture) ListConfigurableProviders() []llm.ConfigurableProvider {
	return append([]llm.ConfigurableProvider{}, fixture.declarations...)
}

func (fixture *llmDirectoryFixture) ListModels(_ context.Context, providerRoute string) ([]llm.ModelInfo, error) {
	if problem := fixture.failures[providerRoute]; problem != nil {
		return nil, problem
	}
	return append([]llm.ModelInfo{}, fixture.catalogs[providerRoute]...), nil
}

func (fixture *llmDirectoryFixture) ResolveModelInfo(
	_ context.Context,
	providerRoute string,
	modelID string,
) (llm.ResolvedModelInfo, error) {
	key := providerRoute + "/" + modelID
	if problem := fixture.failures[key]; problem != nil {
		return llm.ResolvedModelInfo{}, problem
	}
	return fixture.resolved[key], nil
}

func TestLLMGatewayProjectsProviderDirectory(t *testing.T) {
	declared := false
	directory := &llmDirectoryFixture{
		routes: []llm.ProviderInfo{{ID: "deepseek", Name: "DeepSeek"}, {ID: "private", Name: "Private"}},
		declarations: []llm.ConfigurableProvider{
			{Provider: "deepseek", DisplayName: "DeepSeek", SettingsNS: "llm-deepseek", SettingsPath: []string{}},
			{
				Provider: "dormant", DisplayName: "Dormant", SettingsNS: "llm-custom",
				SettingsPath: []string{"providers", "dormant"}, Declared: &declared,
			},
		},
	}
	gateway, err := apiproxy.NewLLMGateway(directory)
	if err != nil {
		t.Fatal(err)
	}
	methods := apiproxy.NewCatalog()
	if err := apiproxy.RegisterLLMAPI(methods, gateway); err != nil {
		t.Fatal(err)
	}
	result, err := methods.DispatchUnary(
		context.Background(), apiproxy.LLMProvidersMethod, connection.RPCID("providers-1"), json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := apiproxy.LLMProvidersValue{Providers: []apiproxy.ConfigurableProviderView{
		{
			Provider: "deepseek", DisplayName: "DeepSeek", SettingsNS: "llm-deepseek",
			SettingsPath: []string{}, Active: true,
		},
		{
			Provider: "dormant", DisplayName: "Dormant", SettingsNS: "llm-custom",
			SettingsPath: []string{"providers", "dormant"}, Active: false, Declared: &declared,
		},
		{Provider: "private", DisplayName: "Private", SettingsNS: "", SettingsPath: []string{}, Active: true},
	}}
	var got apiproxy.LLMProvidersValue
	if err := json.Unmarshal(result.Value, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("providers = %#v, want %#v", got, want)
	}
	directory.declarations[1].SettingsPath[0] = "changed"
	if got.Providers[1].SettingsPath[0] != "providers" {
		t.Fatal("provider settings path aliases the LLM runtime")
	}
}

func TestLLMGatewayContainsProviderCatalogFailures(t *testing.T) {
	directory := &llmDirectoryFixture{
		routes: []llm.ProviderInfo{
			{ID: "deepseek", Name: "DeepSeek"},
			{ID: "broken", Name: "Broken"},
			{ID: "empty", Name: "Empty"},
		},
		catalogs: map[string][]llm.ModelInfo{
			"deepseek": {{Provider: "deepseek", ID: "chat", Name: "Chat", Description: "general"}},
			"empty":    {},
		},
		resolved: map[string]llm.ResolvedModelInfo{
			"deepseek/chat": {
				ModelInfo: llm.ModelInfo{Provider: "deepseek", ID: "chat", Name: "Chat"},
				Reasoning: &llm.ModelReasoningInfo{
					Efforts:       []llm.ReasoningEffortInfo{{ID: "high", Name: "High", Description: "more reasoning"}},
					DefaultEffort: "high",
				},
			},
		},
		failures: map[string]error{"broken": errors.New("catalog unavailable")},
	}
	gateway, err := apiproxy.NewLLMGateway(directory)
	if err != nil {
		t.Fatal(err)
	}
	groups, failures := gateway.Catalog(context.Background())
	wantGroups := []apiproxy.ModelProviderGroup{{
		ID: "deepseek", Name: "DeepSeek",
		Models: []apiproxy.ModelCatalogModel{{
			ID: "chat", Name: "Chat", Description: "general",
			Reasoning: &apiproxy.ModelReasoning{
				Efforts: []apiproxy.ModelReasoningEffort{{
					ID: "high", Name: "High", Description: "more reasoning",
				}},
				DefaultEffort: "high",
			},
		}},
	}}
	wantFailures := []apiproxy.ModelCatalogFailure{{ID: "broken", Name: "Broken", Message: "catalog unavailable"}}
	if !reflect.DeepEqual(groups, wantGroups) {
		t.Fatalf("groups = %#v, want %#v", groups, wantGroups)
	}
	if !reflect.DeepEqual(failures, wantFailures) {
		t.Fatalf("failures = %#v, want %#v", failures, wantFailures)
	}
}

func TestRegisterLLMAPIInstallsTypedMethods(t *testing.T) {
	directory := &llmDirectoryFixture{
		routes:       []llm.ProviderInfo{},
		declarations: []llm.ConfigurableProvider{},
		catalogs:     map[string][]llm.ModelInfo{},
		resolved:     map[string]llm.ResolvedModelInfo{},
		failures:     map[string]error{},
	}
	gateway, err := apiproxy.NewLLMGateway(directory)
	if err != nil {
		t.Fatal(err)
	}
	methods := apiproxy.NewCatalog()
	if err := apiproxy.RegisterLLMAPI(methods, gateway); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{apiproxy.LLMProvidersMethod, apiproxy.LLMModelsMethod} {
		if !methods.HasUnary(method) {
			t.Fatalf("method %q was not registered", method)
		}
		result, err := methods.DispatchUnary(context.Background(), method, connection.RPCID("llm-1"), json.RawMessage(`{"ignored":true}`))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) == "" {
			t.Fatal("empty LLM API response")
		}
	}
}
