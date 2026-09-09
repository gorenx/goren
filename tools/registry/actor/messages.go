// Package toolactor implements the GoAkt boundaries of the Tools capability.
package toolactor

import (
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

// RegistryName is the shared catalog owner's Grain identity.
const RegistryName = "tools-registry"

// InitializeDefaults registers the startup tool configuration without opening providers.
type InitializeDefaults struct {
	Tools []toolregistry.Tool
}

// RegisterConfiguration captures an Agent's complete overrides under a unique update identity.
type RegisterConfiguration struct {
	ID        toolregistry.UpdateID
	AgentID   session.SessionID
	Overrides []toolregistry.Tool
}

// ReadConfiguration requests an exact retained configuration, not the latest catalog.
type ReadConfiguration struct {
	ID toolregistry.UpdateID
}

// Configuration reports a retained configuration. Found remains true if event
// delivery fails after registration; consumers recover through ReadConfiguration.
type Configuration struct {
	Value toolregistry.Configuration
	Found bool
	Err   error
}

// BeginUpdate declares a complete candidate catalog.
type BeginUpdate struct {
	Update toolregistry.Update
}

// RegisterDefinition records one ToolActor's candidate definition.
type RegisterDefinition struct {
	ID        toolregistry.UpdateID
	Candidate toolregistry.Candidate
}

// PublishDefinitions commits a complete batch and emits its definition event.
type PublishDefinitions struct {
	ID toolregistry.UpdateID
}

// ReadDefinitions requests a committed Agent baseline.
type ReadDefinitions struct {
	AgentID session.SessionID
}

// ReadPublication requests an update's commit outcome.
type ReadPublication struct {
	ID toolregistry.UpdateID
}

// AbortUpdate removes an unpublished batch after candidate cleanup.
type AbortUpdate struct {
	ID toolregistry.UpdateID
}

// SettleUpdate releases the update record after switching and resource cleanup.
type SettleUpdate struct {
	ID toolregistry.UpdateID
}

// Completion preserves ordinary business errors.
type Completion struct {
	Err error
}

// Catalog returns a committed view. Err may report notification failure after commit.
type Catalog struct {
	Definitions tools.Definitions
	Found       bool
	Err         error
}

// Outcome distinguishes an unknown update from a preparing or published update.
type Outcome struct {
	Publication toolregistry.Publication
	Found       bool
}
