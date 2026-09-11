package llmactor

import (
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/modelconfig"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// InitializeModels supplies validated initial business state from composition.
type InitializeModels struct {
	Catalog *modelconfig.Catalog
}

// ReadModels requests the current configuration, or validates a specific version.
type ReadModels struct {
	Version uint64
}

// ResolveAgent requests a detached configuration for an Agent.
type ResolveAgent struct {
	Version   uint64
	Overrides llm.CallConfig
}

// ReplaceModels publishes a complete definition if the expected version is current.
type ReplaceModels struct {
	ExpectedVersion uint64
	Definition      modelconfig.Definition
}

// Models preserves business errors without string conversion.
type Models struct {
	Snapshot modelconfig.Snapshot
	Err      error
}

// AgentConfiguration contains model parameters and a credential reference.
type AgentConfiguration struct {
	Resolved modelconfig.Resolved
	Err      error
}

// InitializeAgent restores missing configuration before Registry admits the Agent.
type InitializeAgent struct {
	AgentID    string
	Session    *goakt.GrainIdentity
	CreationID string
}
