package runtimeactor

import (
	"context"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/tools"

	"github.com/gorenx/goren/session"
	toolregistry "github.com/gorenx/goren/tools/registry"
	toolruntime "github.com/gorenx/goren/tools/runtime"
	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// InitializeRuntime binds an Agent creation and applies its initial overrides.
// Only the Agent lifecycle owner starts this Runtime and authorizes its activation.
type InitializeRuntime struct {
	AgentID    session.SessionID
	CreationID string
	Authority  *goakt.GrainIdentity
	Registry   *goakt.GrainIdentity
	Config     *toolregistry.Catalog
	Workers    tool.Workers
	Timeout    time.Duration
}

// CloseRuntime releases only the original activation's configuration and instances.
type CloseRuntime struct {
	CreationID string
	InstanceID string
}

// ReadRuntime requests a settled snapshot without constructing any resources.
type ReadRuntime struct {
	CreationID string
}

// RuntimeView contains only confirmed executable membership.
type RuntimeView struct {
	Snapshot toolruntime.Snapshot
	Err      error
}

// Completion confirms a business operation, including any required cleanup.
type Completion struct {
	Err error
}

// RuntimeName identifies the per-Session tool collection owner.
func RuntimeName(identifier string) string { return identifier + ".tools" }

// PrepareCall captures a call after the Agent has admitted its tool batch.
type PrepareCall struct {
	CreationID string
	Input      tools.ToolExecutionInput
	Context    context.Context
}

// DispatchCall starts only the call admitted by preparation and approval.
type DispatchCall struct {
	CreationID string
	CallID     agentmessage.CallID
}

// FinalizeCall processes the retained outcome, without accepting a supplied result.
type FinalizeCall struct {
	CreationID string
	CallID     agentmessage.CallID
}

// CancelCall requests cancellation without certifying body exit.
type CancelCall struct {
	CreationID string
	CallID     agentmessage.CallID
}

// ReleaseCall releases a settled execution after its consumer accepted the result.
type ReleaseCall struct {
	CreationID string
	CallID     agentmessage.CallID
}

// CallResult distinguishes preparation, final output and unconfirmed failures.
type CallResult struct {
	Stage  tools.ScheduledToolStage
	Result tools.ToolExecutionResult
	Err    error
}

// ReadDefinitions requests the effective schema and visibility baseline.
type ReadDefinitions struct{ CreationID string }

// Definitions reports applied Runtime configuration, never Registry candidates.
type Definitions struct {
	Value tools.Definitions
	Err   error
}

// RefreshConfiguration applies pending Registry input at the caller's Turn boundary.
type RefreshConfiguration struct {
	CreationID string
	ID         string
}

// CancelConfiguration cancels only the named refresh; its original reply confirms cleanup.
type CancelConfiguration struct {
	CreationID string
	ID         string
}
