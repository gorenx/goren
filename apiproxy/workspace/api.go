package workspaceapi

import (
	"context"

	sessionframes "github.com/gorenx/goren/apiproxy/session/events"

	rpc "github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
)

const (
	ListMethod                = "workspace.list"
	CreateMethod              = "workspace.create"
	RenameMethod              = "workspace.rename"
	DeleteMethod              = "workspace.delete"
	InsertBeforeMethod        = "workspace.insertBefore"
	InsertSessionBeforeMethod = "workspace.insertSessionBefore"
	ArchiveSessionMethod      = "workspace.archiveSession"
)

// ListRequest is the empty workspace.list payload.
type ListRequest struct{}

// ListValue is the complete reconnect baseline.
type ListValue struct {
	Items              []View                    `json:"items"`
	ArchivedSessionIDs []sessionframes.SessionID `json:"archivedSessionIds"`
}

// CreateRequest adopts one existing directory.
type CreateRequest struct {
	Path string
}

// CreateValue returns the stable registration and idempotence result.
type CreateValue struct {
	Workspace View `json:"workspace"`
	Created   bool `json:"created"`
}

// RenameRequest changes only a display title.
type RenameRequest struct {
	WorkspaceID ID
	Title       string
}

// RenameValue returns the complete updated Workspace.
type RenameValue struct {
	Workspace View `json:"workspace"`
}

// DeleteRequest identifies one registration to remove.
type DeleteRequest struct {
	WorkspaceID ID
}

// DeleteValue confirms that the registration was removed.
type DeleteValue struct {
	Deleted bool `json:"deleted"`
}

// InsertBeforeRequest moves one Workspace before an optional anchor.
type InsertBeforeRequest struct {
	WorkspaceID       ID
	BeforeWorkspaceID *ID
}

// InsertBeforeValue returns the complete committed order.
type InsertBeforeValue struct {
	WorkspaceIDs []ID `json:"workspaceIds"`
}

// InsertSessionBeforeRequest moves one accounted Session.
type InsertSessionBeforeRequest struct {
	WorkspaceID     ID
	SessionID       sessionframes.SessionID
	BeforeSessionID *sessionframes.SessionID
}

// InsertSessionBeforeValue returns the complete updated Workspace.
type InsertSessionBeforeValue struct {
	Workspace View `json:"workspace"`
}

// ArchiveSessionRequest hides one known Session from grouping surfaces.
type ArchiveSessionRequest struct {
	SessionID sessionframes.SessionID
}

// ArchiveSessionValue returns the complete committed archive set.
type ArchiveSessionValue struct {
	ArchivedSessionIDs []sessionframes.SessionID `json:"archivedSessionIds"`
}

// API owns the typed workspace.* Host surface.
type API interface {
	List(context.Context, rpc.Request[ListRequest]) (rpc.Outcome[ListValue], error)
	Create(context.Context, rpc.Request[CreateRequest]) (rpc.Outcome[CreateValue], error)
	Rename(context.Context, rpc.Request[RenameRequest]) (rpc.Outcome[RenameValue], error)
	Delete(context.Context, rpc.Request[DeleteRequest]) (rpc.Outcome[DeleteValue], error)
	InsertBefore(context.Context, rpc.Request[InsertBeforeRequest]) (rpc.Outcome[InsertBeforeValue], error)
	InsertSessionBefore(context.Context, rpc.Request[InsertSessionBeforeRequest]) (rpc.Outcome[InsertSessionBeforeValue], error)
	ArchiveSession(context.Context, rpc.Request[ArchiveSessionRequest]) (rpc.Outcome[ArchiveSessionValue], error)
}

// Register installs the complete pinned workspace.* method set.
func Register(methods *rpc.Catalog, workspaceMethods API) error {
	registrations := []func() error{
		func() error {
			return rpc.RegisterUnary(methods, ListMethod, DecodeListRequest, workspaceMethods.List)
		},
		func() error {
			return rpc.RegisterUnary(methods, CreateMethod, DecodeCreateRequest, workspaceMethods.Create)
		},
		func() error {
			return rpc.RegisterUnary(methods, RenameMethod, DecodeRenameRequest, workspaceMethods.Rename)
		},
		func() error {
			return rpc.RegisterUnary(methods, DeleteMethod, DecodeDeleteRequest, workspaceMethods.Delete)
		},
		func() error {
			return rpc.RegisterUnary(methods, InsertBeforeMethod, DecodeInsertBeforeRequest, workspaceMethods.InsertBefore)
		},
		func() error {
			return rpc.RegisterUnary(
				methods, InsertSessionBeforeMethod,
				DecodeInsertSessionBeforeRequest, workspaceMethods.InsertSessionBefore,
			)
		},
		func() error {
			return rpc.RegisterUnary(
				methods, ArchiveSessionMethod,
				DecodeArchiveSessionRequest, workspaceMethods.ArchiveSession,
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

func workspaceNotFound[V any](identifier ID) rpc.Outcome[V] {
	return rpc.Fail[V](rpc.NewRPCError(
		connection.ErrorWorkspaceNotFound,
		"workspace \""+string(identifier)+"\" not found",
		struct {
			WorkspaceID ID `json:"workspaceId"`
		}{
			WorkspaceID: identifier,
		},
	))
}
