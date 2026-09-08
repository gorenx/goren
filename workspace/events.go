package workspace

import (
	"github.com/gorenx/goren/session"
)

// ChangedNotice carries one complete committed Workspace state.
type ChangedNotice struct {
	WorkspaceState WorkspaceState
}

func (ChangedNotice) EventName() string { return "workspace/changed" }

// RemovedNotice identifies one committed Workspace deletion.
type RemovedNotice struct {
	ID ID
}

func (RemovedNotice) EventName() string { return "workspace/removed" }

// OrderChangedNotice carries the complete committed display order.
type OrderChangedNotice struct {
	WorkspaceIDs []ID
}

func (OrderChangedNotice) EventName() string { return "workspace/order-changed" }

// ArchivedSessionsChangedNotice carries the complete committed archive set.
type ArchivedSessionsChangedNotice struct {
	SessionIDs []session.SessionID
}

func (ArchivedSessionsChangedNotice) EventName() string {
	return "workspace/archived-sessions-changed"
}

// Topic carries non-durable Workspace notices after successful storage writes.
const Topic = "workspace/notices"

// Notice is the closed vocabulary of post-commit Workspace changes.
type Notice interface {
	EventName() string
	workspaceNotice()
}

func (ChangedNotice) workspaceNotice()                 {}
func (RemovedNotice) workspaceNotice()                 {}
func (OrderChangedNotice) workspaceNotice()            {}
func (ArchivedSessionsChangedNotice) workspaceNotice() {}
