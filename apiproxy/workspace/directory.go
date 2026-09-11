package workspaceapi

import (
	"context"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/workspace"
)

// Directory supplies serialized operations and detached results to the API.
// Mutation results belong to the same committed operation as the request.
type Directory interface {
	List(context.Context) (Listing, error)
	Create(context.Context, string) (workspace.WorkspaceState, bool, error)
	Rename(context.Context, workspace.ID, string) (workspace.WorkspaceState, error)
	Delete(context.Context, workspace.ID) (bool, error)
	InsertBefore(context.Context, workspace.ID, *workspace.ID) ([]workspace.ID, error)
	InsertSessionBefore(context.Context, workspace.ID, session.SessionID, *session.SessionID) (workspace.WorkspaceState, error)
	ArchiveSession(context.Context, session.SessionID) ([]session.SessionID, error)
}

// Listing is one consistent registration and archive snapshot.
type Listing struct {
	Workspaces         []workspace.WorkspaceState
	ArchivedSessionIDs []session.SessionID
}
