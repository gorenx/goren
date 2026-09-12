// Package toolactor adapts the Schema Registry to GoAkt messaging.
package toolactor

import (
	"github.com/gorenx/goren/session"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

// RegistryName is the global schema and policy directory identity.
const RegistryName = "tools-registry"

// RegisterSource replaces the complete schema and policy set from a global source.
type RegisterSource struct {
	Name    string
	Catalog toolregistry.Catalog
}

// RemoveSource withdraws one global source's configuration.
type RemoveSource struct{ Name string }

// ConfigureSession replaces the Session catalog, including its inheritance choice.
type ConfigureSession struct {
	SessionID session.SessionID
	Catalog   toolregistry.Catalog
}

// RemoveSession removes custom configuration and restores inheritance.
type RemoveSession struct{ SessionID session.SessionID }

// ReadCatalog retrieves registered definitions and source policies only.
// The response is toolregistry.Catalog, without user or Session overrides.
type ReadCatalog struct{}

// ReadConfiguration retrieves detached global and custom inputs without merging them.
type ReadConfiguration struct{ SessionID session.SessionID }

// Configuration returns Registry facts; Runtime owns their effective interpretation.
type Configuration struct{ Value toolregistry.Configuration }

// Completion returns the original operation or notification failure.
type Completion struct{ Err error }
