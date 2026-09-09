// Package agentactor defines Agent runtime contracts shared by lifecycle and
// execution actors. It does not depend on a concrete AgentLoop or Registry.
package agentactor

import (
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Claim asks the Agent Registry to bind a reserved creation to one AgentLoop
// activation before that activation reads the Session or acquires resources.
// The first accepted claim fixes the activation identity; repeating that claim
// is allowed, but a different activation cannot take over the same creation.
// Missing or replaced creations and Agents with closing ancestors are rejected,
// as are Agents that are themselves closing. Success authorizes initialization;
// it does not mean initialization has completed or the Agent is ready.
type Claim struct {
	// AgentID is the logical Agent identity, equal to its Session ID.
	AgentID session.SessionID
	// CreationID identifies the Registry reservation. It distinguishes successive
	// Agent lifetimes that reuse the same AgentID.
	CreationID string
	// InstanceID identifies one AgentLoop activation. For example, after instance
	// A claims creation C, a reactivated instance B cannot initialize using C.
	// The coordinator must finish releasing C and reserve a new creation first.
	InstanceID string
	// Executor is the framework route used for lifecycle commands, not a live object.
	Executor *goakt.GrainIdentity
}

// Claimed reports the authorization decision. A nil Err permits initialization;
// a non-nil Err requires the requester to stop without acquiring Agent resources.
type Claimed struct {
	Err error
}

// ClaimTool binds a reserved tool lifetime to one framework activation. CreationID
// identifies the tool reservation, not the containing Agent's creation. InstanceID
// changes on Grain reactivation, which must not silently resume a released tool.
type ClaimTool struct {
	AgentID    session.SessionID
	Name       string
	CreationID string
	InstanceID string
}

// ClaimTools fixes the first authorized Tool Runtime activation of this Agent creation.
// A replacement activation cannot inherit the old runtime's resource responsibilities.
type ClaimTools struct {
	AgentID    session.SessionID
	CreationID string
	InstanceID string
}
