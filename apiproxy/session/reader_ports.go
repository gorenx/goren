package sessionapi

import (
	"context"

	"github.com/gorenx/goren/workspace"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/session/projection"
)

// Snapshot contains detached data used by Session list and history.
// Projections must describe a prefix of Events from the same creation.
type Snapshot struct {
	Header      session.Header
	Events      []session.Event
	Projections projection.Snapshot
	Running     bool
}

// LiveSessions reads initialized creations without activating missing Sessions.
// Read returns nil only when the selected identity has no active creation.
type LiveSessions interface {
	List(context.Context) ([]Snapshot, error)
	Read(context.Context, session.SessionID) (*Snapshot, error)
}

// History supplies validated durable logs and bounded pages, without storage mutation.
type History interface {
	Inspect(context.Context, session.SessionID) (persistence.Inspection, error)
	List(context.Context, persistence.SessionPage) (persistence.HeaderPage, error)
	ReadEventsBefore(context.Context, session.SessionID, persistence.EventPage) (persistence.EventWindow, error)
}

// ProjectionCache supplies rebuildable durable projection checkpoints.
type ProjectionCache interface {
	CachedSnapshot(context.Context, session.Header) (*projection.Snapshot, error)
	ColdSnapshot(context.Context, session.SessionID) (projection.Snapshot, error)
}

// Workspaces supplies detached registration reads and membership mutations.
type Workspaces interface {
	Read(context.Context, workspace.ID) (*workspace.WorkspaceState, error)
	AttachSession(context.Context, workspace.ID, session.SessionID) error
}

// Log is a detached Session history without Agent state or computed projections.
type Log struct {
	Header session.Header
	Events []session.Event
}

// Logs reads admitted Session histories without creating missing sessions.
type Logs interface {
	List(context.Context) ([]Log, error)
	Read(context.Context, session.SessionID) (*Log, error)
}

// Projections computes a view of an explicit log prefix. It must not replace
// another creation's live fold with the caller's potentially older snapshot.
type Projections interface {
	Read(context.Context, []session.Event) (projection.Snapshot, error)
}
