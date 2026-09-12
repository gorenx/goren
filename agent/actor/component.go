package agentactor

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// StartComponent asks a capability Registry to initialize its Agent-local owner.
// Failure does not waive cleanup; StopComponent must confirm resource release.
type StartComponent struct {
	Role       agent.Role
	AgentID    session.SessionID
	CreationID string
	Agent      *goakt.GrainIdentity
	Session    *goakt.GrainIdentity
}

// ComponentStarted confirms the component owner has accepted its lifetime.
// The component contract defines which background work may continue afterward.
type ComponentStarted struct {
	Runtime *goakt.GrainIdentity
	Err     error
}

// StopComponent closes the exact component lifetime, including framework deactivation.
type StopComponent struct {
	Role       agent.Role
	AgentID    session.SessionID
	CreationID string
}

// ComponentStopped confirms business and framework shutdown together.
type ComponentStopped struct {
	Err error
}
