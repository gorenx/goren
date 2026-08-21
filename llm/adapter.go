package llm

import (
	"context"

	"github.com/gorenx/goren/plugin"
)

const (
	PluginName               = "@deepseek-ai/dsh-llm"
	ServiceName              = "llm"
	AdaptersUpdatedEventName = "llm/adapters-updated"
	StreamEventName          = "llm/stream"
)

// AdaptersUpdated announces a committed adapter or configurable-directory change.
type AdaptersUpdated struct{}

// EventName returns the canonical Harness event name.
func (AdaptersUpdated) EventName() string {
	return AdaptersUpdatedEventName
}

// EventDelivery preserves ordered in-process topology notification.
func (AdaptersUpdated) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// Adapter is the required provider-wire capability. Optional metadata
// capabilities below provide the source LlmAdapter class defaults without
// forcing Go implementations to embed a base class.
type Adapter interface {
	Stream(context.Context, GenerateOptions) (ChunkStream, error)
}

// ProviderDescriber overrides the route-key display metadata default.
type ProviderDescriber interface {
	DescribeProvider(string) (ProviderInfo, error)
}

// RetryPolicyProvider overrides the normal source retry defaults for a route.
type RetryPolicyProvider interface {
	ProviderRetryPolicy(string) (RetryPolicy, error)
}

// ModelCatalog advertises advisory provider model metadata.
type ModelCatalog interface {
	ListModels(context.Context, string) ([]ModelInfo, error)
}

// ModelResolver resolves capabilities for one exact provider/model route.
type ModelResolver interface {
	ResolveModel(context.Context, string, string) (ResolvedModelInfo, error)
}

// AdapterRegistrationHandle owns one live adapter route set.
type AdapterRegistrationHandle interface {
	Replace(context.Context, []string) error
	Release(context.Context) error
}

// DirectoryRegistrationHandle owns one live configurable-provider set.
type DirectoryRegistrationHandle interface {
	Replace(context.Context, []ConfigurableProvider) error
	Release(context.Context) error
}

// ModelDiscoveryRegistration owns one live model-discovery binding.
type ModelDiscoveryRegistration interface {
	Release(context.Context) error
}

// PreparedLlmCall binds exact-model resolution and a one-shot dispatch to the
// same adapter registration, preventing a replacement race between logging and I/O.
type PreparedLlmCall interface {
	ConfigValue() CallConfig
	RetryPolicyValue() RetryPolicy
	ContextValue() (ModelContext, bool)
	AdapterDefaultsValue() CallConfigAdapterDefaults
	Stream(context.Context, GenerateOptions) (ChunkStream, error)
}

// LlmRuntime is the provider-owned service contract consumed by Agent and auxiliary calls.
type LlmRuntime interface {
	plugin.Service
	RegisterAdapter(context.Context, []string, Adapter) (AdapterRegistrationHandle, error)
	ListProviders() []ProviderInfo
	RegisterConfigurableProviders(context.Context, []ConfigurableProvider) (DirectoryRegistrationHandle, error)
	ListConfigurableProviders() []ConfigurableProvider
	RegisterModelDiscovery(string, ModelDiscovery) (ModelDiscoveryRegistration, error)
	DiscoverModels(context.Context, string, ModelDiscoveryRequest) ([]DiscoveredModel, error)
	RetryPolicyFor(string) (RetryPolicy, error)
	ListModels(context.Context, string) ([]ModelInfo, error)
	ResolveModelInfo(context.Context, string, string) (ResolvedModelInfo, error)
	ResolveCallConfig(context.Context, CallConfig) (CallConfig, error)
	PrepareCall(context.Context, CallConfig) (PreparedLlmCall, error)
	Stream(context.Context, GenerateOptions) (ChunkStream, error)
}

// ObserverFailureReporter receives contained non-invariant topology-listener failures.
type ObserverFailureReporter interface {
	ReportObserverFailure(error)
}
