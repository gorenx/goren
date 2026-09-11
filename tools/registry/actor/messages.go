// Package toolactor adapts the Schema Registry to GoAkt messaging.
package toolactor

import (
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
)

// RegistryName is the global schema and policy directory identity.
const RegistryName = "tools-registry"

// ConfigurationTopic notifies runtimes that their inherited or custom input changed.
const ConfigurationTopic = "tools/configuration"

// RegisterSource replaces the complete schema and policy set from a global source.
type RegisterSource struct {
	Name    string
	Catalog toolregistry.Catalog
}

// RemoveSource withdraws one global source's configuration.
type RemoveSource struct{ Name string }

// ConfigureTool replaces the complete editable flags of an existing tool.
type ConfigureTool struct {
	Name   string
	Config tools.Config
}

// ConfigureSession replaces the Session's custom schema and policy set.
type ConfigureSession struct {
	SessionID session.SessionID
	Catalog   toolregistry.Catalog
}

// RemoveSession removes custom configuration and restores inheritance.
type RemoveSession struct{ SessionID session.SessionID }

// ReadConfiguration retrieves detached global and custom inputs without merging them.
type ReadConfiguration struct{ SessionID session.SessionID }

// Configuration returns Registry facts; Runtime owns their effective interpretation.
type Configuration struct{ Value toolregistry.Configuration }

// ConfigurationChanged identifies the affected Session; empty means global input changed.
type ConfigurationChanged struct{ SessionID session.SessionID }

// Completion returns the original operation or notification failure.
type Completion struct{ Err error }
