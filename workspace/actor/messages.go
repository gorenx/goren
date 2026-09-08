package workspaceactor

import (
	"context"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/workspace"
)

// Create registers or resolves a canonical directory.
type Create struct{ Path string }

// Created returns the committed registration.
type Created struct {
	State   workspace.WorkspaceState
	Created bool
	Err     error
}

// Read requests a detached workspace snapshot and directory status.
type Read struct{ ID workspace.ID }

// View returns a workspace if it exists.
type View struct {
	State  workspace.WorkspaceState
	Status workspace.Status
	Found  bool
}

// List requests complete registration and archive snapshots.
type List struct{}

// Listing preserves durable workspace ordering.
type Listing struct {
	Workspaces         []workspace.WorkspaceState
	ArchivedSessionIDs []session.SessionID
}

// Delete removes a registration, never its directory or Session logs.
type Delete struct{ ID workspace.ID }

// Deleted reports whether a registration was removed.
type Deleted struct {
	Removed bool
	Err     error
}

// Rename updates the user-facing title.
type Rename struct {
	ID    workspace.ID
	Title string
}

// InsertBefore changes display order; a nil anchor appends.
type InsertBefore struct {
	ID     workspace.ID
	Before *workspace.ID
}

// Order returns committed display order.
type Order struct {
	IDs []workspace.ID
	Err error
}

// AttachSession validates and attaches a known Session.
type AttachSession struct {
	ID        workspace.ID
	SessionID session.SessionID
}

// DetachSession removes a Session membership.
type DetachSession struct {
	ID        workspace.ID
	SessionID session.SessionID
}

// InsertSessionBefore moves an existing member; a nil anchor appends.
type InsertSessionBefore struct {
	ID        workspace.ID
	SessionID session.SessionID
	Before    *session.SessionID
}

// ArchiveSession adds a known Session to the archive set.
type ArchiveSession struct{ SessionID session.SessionID }

// Completion acknowledges a committed mutation or its business error.
type Completion struct{ Err error }

// Command is the closed Workspace operation vocabulary.
type Command interface{ workspaceCommand() }

// Reply is the closed Workspace result vocabulary.
type Reply interface{ workspaceReply() }

// Receiver receives one terminal operation result without blocking the mailbox.
type Receiver interface{ Deliver(Reply) }

// Request transfers one operation to the actor. The framework reply acknowledges
// admission; Receiver gets the actual committed result after I/O has settled.
type Request struct {
	Context  context.Context
	Command  Command
	Receiver Receiver
}

func (Create) workspaceCommand()              {}
func (Read) workspaceCommand()                {}
func (List) workspaceCommand()                {}
func (Delete) workspaceCommand()              {}
func (Rename) workspaceCommand()              {}
func (InsertBefore) workspaceCommand()        {}
func (AttachSession) workspaceCommand()       {}
func (DetachSession) workspaceCommand()       {}
func (InsertSessionBefore) workspaceCommand() {}
func (ArchiveSession) workspaceCommand()      {}
func (Created) workspaceReply()               {}
func (View) workspaceReply()                  {}
func (Listing) workspaceReply()               {}
func (Deleted) workspaceReply()               {}
func (Order) workspaceReply()                 {}
func (Completion) workspaceReply()            {}
