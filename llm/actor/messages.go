package llmactor

import (
	"github.com/gorenx/goren/llm"
)

// RegisterAdapter declares the complete route set for an adapter instance.
type RegisterAdapter struct {
	Providers []string
	Adapter   llm.Adapter
}

// ReplaceAdapter replaces only the identified registration's route set.
type ReplaceAdapter struct {
	ID        llm.RegistrationID
	Providers []string
}

// RemoveAdapter releases one route registration.
type RemoveAdapter struct{ ID llm.RegistrationID }

// RegisterDirectory declares independently configurable providers.
type RegisterDirectory struct{ Entries []llm.ConfigurableProvider }

// ReplaceDirectory replaces one configurable directory registration.
type ReplaceDirectory struct {
	ID      llm.RegistrationID
	Entries []llm.ConfigurableProvider
}

// RemoveDirectory releases a configurable directory registration.
type RemoveDirectory struct{ ID llm.RegistrationID }

// Registered reports the actual registration even if notification fails.
type Registered struct {
	ID  llm.RegistrationID
	Err error
}

// Completion acknowledges a mutation; Changed reports the committed effect.
type Completion struct {
	Changed bool
	Err     error
}

// Select fixes an adapter for a new call without performing network I/O.
type Select struct{ Provider string }

// Selected returns an immutable adapter binding.
type Selected struct {
	Binding *llm.Binding
	Err     error
}

// ReadCatalog requests current active and configurable provider metadata.
type ReadCatalog struct{}

// Catalog contains detached registration metadata.
type Catalog struct {
	Providers    []llm.ProviderInfo
	Configurable []llm.ConfigurableProvider
}
