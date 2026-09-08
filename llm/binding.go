package llm

// Binding fixes a provider route to one adapter instance and its registration metadata.
// It has no reference to the registry; metadata I/O belongs to the calling worker.
type Binding struct {
	backend         Adapter
	metadata        ProviderInfo
	policy          RetryPolicy
	replayProviders []string
}
