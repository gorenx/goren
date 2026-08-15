package workspace

import (
	"fmt"

	"github.com/gorenx/goren/session"
)

// UnknownError reports a mutation against an unregistered Workspace.
type UnknownError struct {
	ID ID
}

func (problem *UnknownError) Error() string {
	return fmt.Sprintf("workspace %q is not registered", problem.ID)
}

// OrderInvalidError reports an unknown Workspace source or anchor.
type OrderInvalidError struct {
	ID ID
}

func (problem *OrderInvalidError) Error() string {
	return fmt.Sprintf("cannot reorder unknown workspace %q", problem.ID)
}

// MoveInvalidError reports an unaccounted Session source or anchor.
type MoveInvalidError struct {
	WorkspaceID     ID
	SessionID       session.SessionID
	BeforeSessionID *session.SessionID
}

func (problem *MoveInvalidError) Error() string {
	if problem.BeforeSessionID == nil {
		return fmt.Sprintf(
			"cannot move session %q in workspace %q: the session is not accounted",
			problem.SessionID, problem.WorkspaceID,
		)
	}
	return fmt.Sprintf(
		"cannot move session %q before %q in workspace %q: the session or anchor is not accounted",
		problem.SessionID, *problem.BeforeSessionID, problem.WorkspaceID,
	)
}

// UnknownSessionError reports an archive request for no live or persisted Session.
type UnknownSessionError struct {
	SessionID session.SessionID
}

func (problem *UnknownSessionError) Error() string {
	return fmt.Sprintf("cannot archive unknown session %q", problem.SessionID)
}

// AttachError reports a Session whose immutable cwd cannot establish membership.
type AttachError struct {
	WorkspaceID ID
	SessionID   session.SessionID
	Reason      string
}

func (problem *AttachError) Error() string {
	return fmt.Sprintf(
		"cannot attach session %q to workspace %q: %s",
		problem.SessionID, problem.WorkspaceID, problem.Reason,
	)
}
