// Package workspace owns durable project-directory registrations and their
// ordered Session accounts. It does not own project files or Session logs.
package workspace

import (
	"context"
	"time"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// ID is the stable identity of one registered project directory.
type ID string

// StoredWorkspace is the durable state of one Workspace registration.
type StoredWorkspace struct {
	ID         ID
	Path       string
	Title      string
	SessionIDs []session.SessionID
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// WorkspaceState is a detached, consumer-safe view of one Workspace.
type WorkspaceState struct {
	ID         ID
	Path       string
	Title      string
	SessionIDs []session.SessionID
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// StoredRegistry is the complete data owned by Workspace persistence.
type StoredRegistry struct {
	Initialized        bool
	WorkspaceIDs       []ID
	ArchivedSessionIDs []session.SessionID
	Records            []StoredWorkspace
}

// Backend is the consumer-owned, storage-only persistence port. Its
// implementations execute requested atomic writes but contain no Workspace
// membership, ordering, or filesystem policy.
type Backend interface {
	Load(context.Context) (StoredRegistry, error)
	Initialize(context.Context, StoredRegistry) error
	Create(context.Context, StoredWorkspace, []ID) error
	Update(context.Context, StoredWorkspace) error
	Delete(context.Context, ID, []ID) error
	SetOrder(context.Context, []ID) error
	SetArchivedSessionIDs(context.Context, []session.SessionID) error
	Close(context.Context) error
}

// SessionHeaders is the Workspace-owned anti-corruption port for immutable
// Session headers. Implementations may merge live and persisted Sessions.
type SessionHeaders interface {
	Get(context.Context, session.SessionID) (session.Header, bool, error)
	List(context.Context) ([]session.Header, error)
}

// Workspace exposes one registered project directory without its storage details.
type Workspace interface {
	Snapshot() WorkspaceState
	SetTitle(context.Context, string) error
	AttachSession(context.Context, session.SessionID) error
	InsertSessionBefore(context.Context, session.SessionID, *session.SessionID) error
	DetachSession(context.Context, session.SessionID) error
	Status() Status
}

// Registry owns Workspace identity, durable order, and Session accounting.
type Registry interface {
	Create(context.Context, string) (Workspace, bool, error)
	Get(ID) (Workspace, bool)
	List() []Workspace
	Delete(context.Context, ID) (bool, error)
	InsertBefore(context.Context, ID, *ID) ([]ID, error)
	ArchivedSessionIDs() []session.SessionID
	ArchiveSession(context.Context, session.SessionID) error
}

// Service is the canonical Workspace Registry service definition.
var Service = plugin.DefineService[Registry]("workspaceRegistry")

// Status reports whether a registered project directory is usable now.
type Status string

const (
	StatusOK         Status = "ok"
	StatusMissingDir Status = "missing-dir"
)

// RegistryOptions supplies process dependencies and deterministic test seams.
type RegistryOptions struct {
	Clock         func() time.Time
	NewID         func() (ID, error)
	ObserverError func(error)
}
