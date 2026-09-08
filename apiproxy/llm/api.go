package llmapi

import rpc "github.com/gorenx/goren/apiproxy/rpc"

import "context"

const (
	ProvidersMethod = "llm.providers"
	ModelsMethod    = "llm.models"
)

// ProvidersRequest is the empty llm.providers payload.
type ProvidersRequest struct{}

// ConfigurableProviderView is one configuration-facing provider route.
type ConfigurableProviderView struct {
	Provider     string   `json:"provider"`
	DisplayName  string   `json:"displayName"`
	SettingsNS   string   `json:"settingsNs"`
	SettingsPath []string `json:"settingsPath"`
	Active       bool     `json:"active"`
	Declared     *bool    `json:"declared,omitempty"`
}

// ProvidersValue is the provider directory in declaration order.
type ProvidersValue struct {
	Providers []ConfigurableProviderView `json:"providers"`
}

// ModelsRequest is the empty llm.models payload.
type ModelsRequest struct{}

// ModelsValue is the Host-scoped model catalog.
type ModelsValue struct {
	Groups   []ModelProviderGroup  `json:"groups"`
	Failures []ModelCatalogFailure `json:"failures"`
}

// API owns the Host-scoped provider and model catalog methods.
type API interface {
	Providers(context.Context, rpc.Request[ProvidersRequest]) (rpc.Outcome[ProvidersValue], error)
	Models(context.Context, rpc.Request[ModelsRequest]) (rpc.Outcome[ModelsValue], error)
}

// Register installs the included Host-scoped LLM catalog methods.
func Register(methods *rpc.Catalog, endpoint API) error {
	if err := rpc.RegisterUnary(methods, ProvidersMethod, rpc.DecodeObject[ProvidersRequest], endpoint.Providers); err != nil {
		return err
	}
	return rpc.RegisterUnary(methods, ModelsMethod, rpc.DecodeObject[ModelsRequest], endpoint.Models)
}
