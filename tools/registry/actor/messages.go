// Package toolactor implements the GoAkt boundaries of the Tools capability.
package toolactor

import (
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

// RegistryName is the shared catalog owner's Grain identity.
const RegistryName = "tools-registry"

// Begin declares a complete candidate catalog.
type Begin struct {
	Update toolregistry.Update
}

// Register records one ToolActor's candidate definition.
type Register struct {
	ID        toolregistry.UpdateID
	Candidate toolregistry.Candidate
}

// Publish commits a complete batch and emits its definition event.
type Publish struct {
	ID toolregistry.UpdateID
}

// Read requests a committed Agent baseline.
type Read struct {
	AgentID session.SessionID
}

// Lookup requests an update's commit outcome.
type Lookup struct {
	ID toolregistry.UpdateID
}

// Abort removes an unpublished batch after candidate cleanup.
type Abort struct {
	ID toolregistry.UpdateID
}

// Settle releases the update record after switching and resource cleanup.
type Settle struct {
	ID toolregistry.UpdateID
}

// Completion preserves ordinary business errors.
type Completion struct {
	Err error
}

// Catalog returns a committed view. Err may report notification failure after commit.
type Catalog struct {
	Definitions tools.DefinitionsChanged
	Found       bool
	Err         error
}

// Outcome distinguishes an unknown update from a preparing or published update.
type Outcome struct {
	Publication toolregistry.Publication
	Found       bool
}
