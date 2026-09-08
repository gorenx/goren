package llm

import (
	"context"
)

const (
	AdaptersUpdatedEventName = "llm/adapters-updated"
	StreamEventName          = "llm/stream"
)

// AdaptersUpdated announces a committed adapter or configurable-directory change.
type AdaptersUpdated struct{}

// EventName returns the canonical Harness event name.
func (AdaptersUpdated) EventName() string {
	return AdaptersUpdatedEventName
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
