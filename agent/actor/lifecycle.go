package agentactor

import (
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Initialize binds already-started component routes and restores execution state.
// The lifecycle Registry owns their startup and shutdown.
type Initialize struct {
	AgentID     session.SessionID
	Authority   *goakt.GrainIdentity
	CreationID  string
	Session     *goakt.GrainIdentity
	Prompt      *goakt.GrainIdentity
	ModelConfig *goakt.GrainIdentity
}

// Initialized acknowledges completed Agent initialization, not just activation or Claim.
type Initialized struct {
	Err error
}

// PrepareClose stops new input admission and checks whether accepted work has finished.
// A non-nil Prepared.Err means resources must remain available for completion.
type PrepareClose struct {
	CreationID string
}

// Prepared reports whether accepted work has finished, not resource shutdown.
type Prepared struct {
	Err error
}

// CloseResources releases this Agent's resources after preparation and after
// the Registry has confirmed that its children have closed.
type CloseResources struct {
	CreationID string
}

// Closed confirms resource shutdown only when Err is nil. The Registry still
// owns final Agent actor deactivation and release of the Agent reservation.
type Closed struct {
	Err error
}

// Verify checks the exact initialized activation before publishing a runtime route.
// A stable Grain address alone cannot distinguish an unexpected reactivation.
type Verify struct {
	CreationID string
	InstanceID string
}

// Verified acknowledges the activation check without starting business work.
type Verified struct{ Err error }
