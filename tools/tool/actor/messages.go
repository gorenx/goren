// Package toolactor schedules a single tool's resource operations and registration.
package toolactor

import (
	"context"
	"encoding/hex"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Initialize binds a tool reservation to the catalog and lifecycle owners.
// CreationID is the tool's lifetime, not the containing Agent's lifetime.
type Initialize struct {
	AgentCreationID string
	CreationID      string
	AgentID         session.SessionID
	Name            string
	Authority       *goakt.GrainIdentity
}

// RuntimeName encodes the tool segment so logical names containing dots cannot
// collide with another Session's service address.
func RuntimeName(agentID string, toolName string) string {
	return agentID + ".tool." + hex.EncodeToString([]byte(toolName))
}

// Provision constructs a candidate under the caller-owned update boundary.
// ProviderID must match the implementation selected from process-local composition.
type Provision struct {
	CreationID string
	Revision   tool.Revision
	Schema     llm.ToolSchema
	ProviderID tools.ProviderID
	Context    context.Context
}

// Commit switches to a candidate after Runtime has prepared all replacement work.
type Commit struct {
	CreationID string
	Revision   tool.Revision
}

// Retire releases the previous implementation after switching.
type Retire struct {
	CreationID string
	Revision   tool.Revision
	Context    context.Context
}

// Abort releases a candidate after the caller rules out catalog publication.
type Abort struct {
	CreationID string
	Revision   tool.Revision
	Context    context.Context
}

// Close releases all implementations after the caller has drained executions.
type Close struct {
	CreationID string
	Context    context.Context
}

// Completion is a terminal operation result, not an admission acknowledgement.
type Completion struct {
	Err error
}

// Execute dispatches a body after Runtime policy, approval and middleware admission.
// Its reply is tools.BodyResult, not a finalized ToolExecutionResult.
type Execute struct {
	CreationID string
	Revision   tool.Revision
	Invocation tools.Invocation
	Context    context.Context
}

// CancelCall signals cancellation; the original Execute reply proves body exit.
type CancelCall struct {
	CreationID string
	CallID     agentmessage.CallID
}

// Describe requests the execution contract of one settled implementation.
type Describe struct {
	CreationID string
}

// Description returns no executor or owned resource to the caller.
type Description struct {
	Revision   tool.Revision
	Descriptor tools.Descriptor
	Err        error
}

// Claim binds a reserved tool to its first physical activation in Tool Runtime.
// A fresh activation cannot assume a prior activation's cleanup responsibility.
type Claim struct {
	AgentCreationID string
	AgentID         session.SessionID
	Name            string
	CreationID      string
	InstanceID      string
}
