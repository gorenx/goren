package workspaceadapter

import (
	"context"
	"errors"

	workspaceapi "github.com/gorenx/goren/apiproxy/workspace"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/workspace"
	workspaceactor "github.com/gorenx/goren/workspace/actor"
)

// Directory adapts the Workspace mailbox to the API's operation port.
type Directory struct{ endpoint *workspaceactor.Client }

// NewDirectory binds an already-started Workspace actor client.
func NewDirectory(endpoint *workspaceactor.Client) (*Directory, error) {
	if endpoint == nil {
		return nil, errors.New("workspace API: client is required")
	}
	return &Directory{
		endpoint: endpoint,
	}, nil
}

func (adapter *Directory) List(ctx context.Context) (workspaceapi.Listing, error) {
	result, err := adapter.endpoint.List(ctx)
	return workspaceapi.Listing{
		Workspaces:         result.Workspaces,
		ArchivedSessionIDs: result.ArchivedSessionIDs,
	}, err
}
func (adapter *Directory) Create(ctx context.Context, path string) (workspace.WorkspaceState, bool, error) {
	return adapter.endpoint.Create(ctx, path)
}
func (adapter *Directory) Rename(ctx context.Context, identifier workspace.ID, title string) (workspace.WorkspaceState, error) {
	return adapter.endpoint.Rename(ctx, workspaceactor.Rename{
		ID:    identifier,
		Title: title,
	})
}
func (adapter *Directory) Delete(ctx context.Context, identifier workspace.ID) (bool, error) {
	return adapter.endpoint.Delete(ctx, identifier)
}
func (adapter *Directory) InsertBefore(ctx context.Context, identifier workspace.ID, before *workspace.ID) ([]workspace.ID, error) {
	return adapter.endpoint.InsertBefore(ctx, workspaceactor.InsertBefore{
		ID:     identifier,
		Before: before,
	})
}
func (adapter *Directory) InsertSessionBefore(ctx context.Context, identifier workspace.ID, selected session.SessionID, before *session.SessionID) (workspace.WorkspaceState, error) {
	return adapter.endpoint.InsertSessionBefore(ctx, workspaceactor.InsertSessionBefore{
		ID:        identifier,
		SessionID: selected,
		Before:    before,
	})
}
func (adapter *Directory) ArchiveSession(ctx context.Context, selected session.SessionID) ([]session.SessionID, error) {
	return adapter.endpoint.ArchiveSession(ctx, workspaceactor.ArchiveSession{
		SessionID: selected,
	})
}

var _ workspaceapi.Directory = (*Directory)(nil)
