package llmapi_test

import (
	"context"
	"encoding/json"
	"errors"
	rpc "github.com/gorenx/goren/apiproxy/rpc"
	"reflect"
	"testing"

	api "github.com/gorenx/goren/apiproxy/llm"
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

func (fixture *llmDirectoryFixture) ReadCatalog(context.Context) (api.ProviderCatalog, error) {
	if failure := fixture.failures["catalog"]; failure != nil {
		return api.ProviderCatalog{}, failure
	}
	return api.ProviderCatalog{
		Active:       append([]llm.ProviderInfo{}, fixture.routes...),
		Configurable: append([]llm.ConfigurableProvider{}, fixture.declarations...),
	}, nil
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
		routes: []llm.ProviderInfo{{
			ID:   "deepseek",
			Name: "DeepSeek",
		}, {
			ID:   "private",
			Name: "Private",
		}},
		declarations: []llm.ConfigurableProvider{
			{
				Provider:     "deepseek",
				DisplayName:  "DeepSeek",
				SettingsNS:   "llm-deepseek",
				SettingsPath: []string{},
			},
			{
				Provider:     "dormant",
				DisplayName:  "Dormant",
				SettingsNS:   "llm-custom",
				SettingsPath: []string{"providers", "dormant"},
				Declared:     &declared,
			},
		},
	}
	gateway, err := api.NewGateway(directory)
	if err != nil {
		t.Fatal(err)
	}
	methods := rpc.NewCatalog()
	if err := api.Register(methods, gateway); err != nil {
		t.Fatal(err)
	}
	result, err := methods.DispatchUnary(
		context.Background(), api.ProvidersMethod, connection.RPCID("providers-1"), json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := api.ProvidersValue{
		Providers: []api.ConfigurableProviderView{
			{
				Provider:     "deepseek",
				DisplayName:  "DeepSeek",
				SettingsNS:   "llm-deepseek",
				SettingsPath: []string{},
				Active:       true,
			},
			{
				Provider:     "dormant",
				DisplayName:  "Dormant",
				SettingsNS:   "llm-custom",
				SettingsPath: []string{"providers", "dormant"},
				Active:       false,
				Declared:     &declared,
			},
			{
				Provider:     "private",
				DisplayName:  "Private",
				SettingsNS:   "",
				SettingsPath: []string{},
				Active:       true,
			},
		},
	}
	var got api.ProvidersValue
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
			{
				ID:   "deepseek",
				Name: "DeepSeek",
			},
			{
				ID:   "broken",
				Name: "Broken",
			},
			{
				ID:   "empty",
				Name: "Empty",
			},
		},
		catalogs: map[string][]llm.ModelInfo{
			"deepseek": {{
				Provider:    "deepseek",
				ID:          "chat",
				Name:        "Chat",
				Description: "general",
			}},

			"empty": {},
		},
		resolved: map[string]llm.ResolvedModelInfo{
			"deepseek/chat": {
				ModelInfo: llm.ModelInfo{
					Provider: "deepseek",
					ID:       "chat",
					Name:     "Chat",
				},
				Reasoning: &llm.ModelReasoningInfo{
					Efforts: []llm.ReasoningEffortInfo{{
						ID:          "high",
						Name:        "High",
						Description: "more reasoning",
					}},
					DefaultEffort: "high",
				},
			},
		},
		failures: map[string]error{
			"broken": errors.New("catalog unavailable"),
		},
	}
	gateway, err := api.NewCatalog(directory)
	if err != nil {
		t.Fatal(err)
	}
	groups, failures, err := gateway.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantGroups := []api.ModelProviderGroup{{
		ID:   "deepseek",
		Name: "DeepSeek",
		Models: []api.ModelCatalogModel{{
			ID:          "chat",
			Name:        "Chat",
			Description: "general",
			Reasoning: &api.ModelReasoning{
				Efforts: []api.ModelReasoningEffort{{
					ID:          "high",
					Name:        "High",
					Description: "more reasoning",
				}},
				DefaultEffort: "high",
			},
		}},
	}}
	wantFailures := []api.ModelCatalogFailure{{
		ID:      "broken",
		Name:    "Broken",
		Message: "catalog unavailable",
	}}
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
	gateway, err := api.NewGateway(directory)
	if err != nil {
		t.Fatal(err)
	}
	methods := rpc.NewCatalog()
	if err := api.Register(methods, gateway); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{api.ProvidersMethod, api.ModelsMethod} {
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

func TestLLMGatewayDoesNotHideCatalogFailure(t *testing.T) {
	failure := errors.New("registry unavailable")
	gateway, err := api.NewGateway(&llmDirectoryFixture{
		failures: map[string]error{
			"catalog": failure,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.Providers(context.Background(), rpc.Request[api.ProvidersRequest]{}); !errors.Is(err, failure) {
		t.Fatalf("providers failure: %v", err)
	}
	if _, err := gateway.Models(context.Background(), rpc.Request[api.ModelsRequest]{}); !errors.Is(err, failure) {
		t.Fatalf("models failure: %v", err)
	}
}
