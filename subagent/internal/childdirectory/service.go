// Package childdirectory implements read-only durable Subagent discovery without
// loading or resuming Agents.
package childdirectory

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sessproj "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/subagent"
)

// Sessions is the live listing capability consumed by ChildDirectory.
type Sessions interface {
	List() []session.Context
}

// Persistence is the optional cold listing capability consumed by ChildDirectory.
type Persistence interface {
	List(context.Context, sesspersist.SessionPage) (sesspersist.HeaderPage, error)
	Inspect(context.Context, session.SessionID) (sesspersist.Inspection, error)
}

// Projections is the identity fold capability consumed by ChildDirectory.
type Projections interface {
	Snapshot(session.Context) (sessproj.Snapshot, error)
	Restore(
		sessproj.Checkpoint,
		[]session.Event,
		int64,
	) (sessproj.RestoreResult, error)
}

// ProjectionCache is the optional zero-log-I/O checkpoint view consumed by
// cold child resolution.
type ProjectionCache interface {
	CachedSnapshot(
		session.Header,
	) (*sessproj.Snapshot, error)
}

type dependencies struct {
	sessions    Sessions
	persistence Persistence
	projections Projections
	cache       ProjectionCache
}

// Service is the stable ChildDirectory capability published by the Subagent Plugin.
type Service struct {
	mutex  sync.RWMutex
	active *dependencies
}

// New constructs a disabled ChildDirectory Service.
func New() *Service {
	return &Service{}
}

// Enable installs the dependencies resolved for one Plugin Apply/Dispose cycle.
func (owner *Service) Enable(
	liveSource Sessions,
	durabilitySource Persistence,
	projectionSource Projections,
	checkpointSource ProjectionCache,
) error {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.active != nil {
		return errors.New("subagent: ChildDirectory Service is already enabled")
	}
	owner.active = &dependencies{
		sessions:    liveSource,
		persistence: durabilitySource,
		projections: projectionSource,
		cache:       checkpointSource,
	}
	return nil
}

// Disable removes dependencies retained for the current Plugin cycle.
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
		return errors.New("subagent: ChildDirectory Context is nil")
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

var _ subagent.ChildDirectory = (*Service)(nil)
