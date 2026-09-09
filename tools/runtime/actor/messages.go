package runtimeactor

import (
	"context"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/execution"

	"github.com/gorenx/goren/session"
	toolregistry "github.com/gorenx/goren/tools/registry"
	toolruntime "github.com/gorenx/goren/tools/runtime"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// InitializeRuntime binds an Agent creation and applies its initial overrides.
// Only the Agent lifecycle owner starts this Runtime and authorizes its activation.
type InitializeRuntime struct {
	AgentID    session.SessionID
	CreationID string
	Authority  *goakt.GrainIdentity
	Registry   *goakt.GrainIdentity
	Overrides  []toolregistry.Tool
	Policies   execution.Policies
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

// ApplyCallApproval supplies the approval owner's audited decision.
type ApplyCallApproval struct {
	CreationID string
	CallID     agentmessage.CallID
	Outcome    approval.Outcome
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
	Stage    tools.ScheduledToolStage
	Approval *tools.ApprovalRequest
	Result   tools.ToolExecutionResult
	Err      error
}
