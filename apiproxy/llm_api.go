package apiproxy

import "context"

const (
	LLMProvidersMethod = "llm.providers"
	LLMModelsMethod    = "llm.models"
)

// LLMProvidersRequest is the empty llm.providers payload.
type LLMProvidersRequest struct{}

// ConfigurableProviderView is one configuration-facing provider route.
type ConfigurableProviderView struct {
	Provider     string   `json:"provider"`
	DisplayName  string   `json:"displayName"`
	SettingsNS   string   `json:"settingsNs"`
	SettingsPath []string `json:"settingsPath"`
	Active       bool     `json:"active"`
	Declared     *bool    `json:"declared,omitempty"`
}

// LLMProvidersValue is the provider directory in declaration order.
type LLMProvidersValue struct {
	Providers []ConfigurableProviderView `json:"providers"`
}

// LLMModelsRequest is the empty llm.models payload.
type LLMModelsRequest struct{}

// LLMModelsValue is the Host-scoped model catalog.
type LLMModelsValue struct {
	Groups   []ModelProviderGroup  `json:"groups"`
	Failures []ModelCatalogFailure `json:"failures"`
}

// LLMAPI owns the Host-scoped provider and model catalog methods.
type LLMAPI interface {
	Providers(context.Context, Request[LLMProvidersRequest]) (Outcome[LLMProvidersValue], error)
	Models(context.Context, Request[LLMModelsRequest]) (Outcome[LLMModelsValue], error)
}

// RegisterLLMAPI installs the included Host-scoped LLM catalog methods.
func RegisterLLMAPI(methods *Catalog, gateway LLMAPI) error {
	if err := RegisterUnary(methods, LLMProvidersMethod, DecodeObject[LLMProvidersRequest], gateway.Providers); err != nil {
		return err
	}
	return RegisterUnary(methods, LLMModelsMethod, DecodeObject[LLMModelsRequest], gateway.Models)
}
