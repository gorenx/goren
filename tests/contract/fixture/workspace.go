//go:build contract

package fixture

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/workspace"
)

// EmptyWorkspaces is the valid empty Workspace baseline used by contract
// scenarios that exercise APIs unrelated to Workspace.
type EmptyWorkspaces struct{}

func (EmptyWorkspaces) Create(context.Context, string) (workspace.Workspace, bool, error) {
	return nil, false, errors.New("fixture: Workspace creation is unavailable")
}

func (EmptyWorkspaces) Get(workspace.ID) (workspace.Workspace, bool) { return nil, false }

func (EmptyWorkspaces) List() []workspace.Workspace { return []workspace.Workspace{} }

func (EmptyWorkspaces) Delete(context.Context, workspace.ID) (bool, error) { return false, nil }

func (EmptyWorkspaces) InsertBefore(context.Context, workspace.ID, *workspace.ID) ([]workspace.ID, error) {
	return nil, errors.New("fixture: Workspace reorder is unavailable")
}

func (EmptyWorkspaces) ArchivedSessionIDs() []session.SessionID { return []session.SessionID{} }

func (EmptyWorkspaces) ArchiveSession(context.Context, session.SessionID) error {
	return errors.New("fixture: Session archive is unavailable")
}
