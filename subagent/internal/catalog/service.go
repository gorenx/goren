// Package catalog implements read-only durable Subagent discovery without
// loading or resuming Agents.
package catalog

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/session"
	sessionpersistence "github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/subagent"
)

// Sessions is the live listing capability consumed by Catalog.
type Sessions interface {
	List() []*session.Session
}

// Persistence is the optional cold listing capability consumed by Catalog.
type Persistence interface {
	List(context.Context) ([]session.Header, error)
	Inspect(context.Context, session.SessionID) (sessionpersistence.Inspection, error)
}

// Projections is the identity fold capability consumed by Catalog.
type Projections interface {
	Snapshot(*session.Session) (sessionprojection.Snapshot, error)
	Restore(
		sessionprojection.Checkpoint,
		[]session.Event,
		int64,
	) (sessionprojection.RestoreResult, error)
}

type dependencies struct {
	sessions    Sessions
	persistence Persistence
	projections Projections
}

// Service is the stable Catalog capability published by the Subagent Plugin.
type Service struct {
	mutex  sync.RWMutex
	active *dependencies
}

// New constructs a disabled Catalog Service.
func New() *Service {
	return &Service{}
}

// Enable installs the dependencies resolved for one Plugin activation.
func (owner *Service) Enable(
	liveSource Sessions,
	durabilitySource Persistence,
	projectionSource Projections,
) error {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.active != nil {
		return errors.New("subagent: Catalog Service is already enabled")
	}
	owner.active = &dependencies{
		sessions:    liveSource,
		persistence: durabilitySource,
		projections: projectionSource,
	}
	return nil
}

// Disable removes dependencies retained for the current Plugin activation.
func (owner *Service) Disable() {
	owner.mutex.Lock()
	owner.active = nil
	owner.mutex.Unlock()
}

func (owner *Service) snapshot() dependencies {
	owner.mutex.RLock()
	defer owner.mutex.RUnlock()
	if owner.active == nil {
		return dependencies{}
	}
	return *owner.active
}

func requireListing(
	requestContext context.Context,
	dependencySet dependencies,
) error {
	if requestContext == nil {
		return errors.New("subagent: Catalog Context is nil")
	}
	if requestErr := requestContext.Err(); requestErr != nil {
		return cancelled(requestErr)
	}
	if dependencySet.projections == nil {
		return &subagent.Error{
			Code:    subagent.ErrorControlProjectionsUnavailable,
			Message: "listing subagents requires the Session Projection Registry",
		}
	}
	if dependencySet.sessions == nil {
		return &subagent.Error{
			Code:    subagent.ErrorControlSessionStoreUnavailable,
			Message: "listing subagents requires the Session LiveStore",
		}
	}
	return nil
}

func cancelled(cause error) error {
	return &subagent.Error{
		Code:    subagent.ErrorCancelled,
		Message: "subagent listing was cancelled",
		Cause:   cause,
	}
}

var _ subagent.Catalog = (*Service)(nil)
