package apiproxy

import (
	"context"

	"github.com/gorenx/goren/connection"
)

const (
	WorkspaceListMethod                = "workspace.list"
	WorkspaceCreateMethod              = "workspace.create"
	WorkspaceRenameMethod              = "workspace.rename"
	WorkspaceDeleteMethod              = "workspace.delete"
	WorkspaceInsertBeforeMethod        = "workspace.insertBefore"
	WorkspaceInsertSessionBeforeMethod = "workspace.insertSessionBefore"
	WorkspaceArchiveSessionMethod      = "workspace.archiveSession"
)

// WorkspaceListRequest is the empty workspace.list payload.
type WorkspaceListRequest struct{}

// WorkspaceListValue is the complete reconnect baseline.
type WorkspaceListValue struct {
	Items              []WorkspaceView `json:"items"`
	ArchivedSessionIDs []SessionID     `json:"archivedSessionIds"`
}

// WorkspaceCreateRequest adopts one existing directory.
type WorkspaceCreateRequest struct {
	Path string
}

// WorkspaceCreateValue returns the stable registration and idempotence result.
type WorkspaceCreateValue struct {
	Workspace WorkspaceView `json:"workspace"`
	Created   bool          `json:"created"`
}

// WorkspaceRenameRequest changes only a display title.
type WorkspaceRenameRequest struct {
	WorkspaceID WorkspaceID
	Title       string
}

// WorkspaceRenameValue returns the complete updated Workspace.
type WorkspaceRenameValue struct {
	Workspace WorkspaceView `json:"workspace"`
}

// WorkspaceDeleteRequest identifies one registration to remove.
type WorkspaceDeleteRequest struct {
	WorkspaceID WorkspaceID
}

// WorkspaceDeleteValue confirms that the registration was removed.
type WorkspaceDeleteValue struct {
	Deleted bool `json:"deleted"`
}

// WorkspaceInsertBeforeRequest moves one Workspace before an optional anchor.
type WorkspaceInsertBeforeRequest struct {
	WorkspaceID       WorkspaceID
	BeforeWorkspaceID *WorkspaceID
}

// WorkspaceInsertBeforeValue returns the complete committed order.
type WorkspaceInsertBeforeValue struct {
	WorkspaceIDs []WorkspaceID `json:"workspaceIds"`
}

// WorkspaceInsertSessionBeforeRequest moves one accounted Session.
type WorkspaceInsertSessionBeforeRequest struct {
	WorkspaceID     WorkspaceID
	SessionID       SessionID
	BeforeSessionID *SessionID
}

// WorkspaceInsertSessionBeforeValue returns the complete updated Workspace.
type WorkspaceInsertSessionBeforeValue struct {
	Workspace WorkspaceView `json:"workspace"`
}

// WorkspaceArchiveSessionRequest hides one known Session from grouping surfaces.
type WorkspaceArchiveSessionRequest struct {
	SessionID SessionID
}

// WorkspaceArchiveSessionValue returns the complete committed archive set.
type WorkspaceArchiveSessionValue struct {
	ArchivedSessionIDs []SessionID `json:"archivedSessionIds"`
}

// WorkspaceAPI owns the typed workspace.* Host surface.
type WorkspaceAPI interface {
	List(context.Context, Request[WorkspaceListRequest]) (Outcome[WorkspaceListValue], error)
	Create(context.Context, Request[WorkspaceCreateRequest]) (Outcome[WorkspaceCreateValue], error)
	Rename(context.Context, Request[WorkspaceRenameRequest]) (Outcome[WorkspaceRenameValue], error)
	Delete(context.Context, Request[WorkspaceDeleteRequest]) (Outcome[WorkspaceDeleteValue], error)
	InsertBefore(context.Context, Request[WorkspaceInsertBeforeRequest]) (Outcome[WorkspaceInsertBeforeValue], error)
	InsertSessionBefore(context.Context, Request[WorkspaceInsertSessionBeforeRequest]) (Outcome[WorkspaceInsertSessionBeforeValue], error)
	ArchiveSession(context.Context, Request[WorkspaceArchiveSessionRequest]) (Outcome[WorkspaceArchiveSessionValue], error)
}

// RegisterWorkspaceAPI installs the complete pinned workspace.* method set.
func RegisterWorkspaceAPI(methods *Catalog, service WorkspaceAPI) error {
	registrations := []func() error{
		func() error {
			return RegisterUnary(methods, WorkspaceListMethod, DecodeWorkspaceListRequest, service.List)
		},
		func() error {
			return RegisterUnary(methods, WorkspaceCreateMethod, DecodeWorkspaceCreateRequest, service.Create)
		},
		func() error {
			return RegisterUnary(methods, WorkspaceRenameMethod, DecodeWorkspaceRenameRequest, service.Rename)
		},
		func() error {
			return RegisterUnary(methods, WorkspaceDeleteMethod, DecodeWorkspaceDeleteRequest, service.Delete)
		},
		func() error {
			return RegisterUnary(methods, WorkspaceInsertBeforeMethod, DecodeWorkspaceInsertBeforeRequest, service.InsertBefore)
		},
		func() error {
			return RegisterUnary(
				methods, WorkspaceInsertSessionBeforeMethod,
				DecodeWorkspaceInsertSessionBeforeRequest, service.InsertSessionBefore,
			)
		},
		func() error {
			return RegisterUnary(
				methods, WorkspaceArchiveSessionMethod,
				DecodeWorkspaceArchiveSessionRequest, service.ArchiveSession,
			)
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

func workspaceNotFound[V any](identifier WorkspaceID) Outcome[V] {
	return Fail[V](newRPCError(
		connection.ErrorWorkspaceNotFound,
		"workspace \""+string(identifier)+"\" not found",
		struct {
			WorkspaceID WorkspaceID `json:"workspaceId"`
		}{WorkspaceID: identifier},
	))
}
