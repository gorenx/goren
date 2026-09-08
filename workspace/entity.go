package workspace

import (
	"context"
	"os"

	"github.com/gorenx/goren/session"
)

// entity is the Registry-backed implementation of Workspace. It retains only
// stable identity so every read observes the latest committed record.
type entity struct {
	owner      *DurableRegistry
	identifier ID
}

func (subject *entity) Snapshot() WorkspaceState {
	state, _ := subject.owner.readState(subject.identifier)
	return state
}

func (subject *entity) SetTitle(requestContext context.Context, title string) error {
	return subject.owner.SetTitle(requestContext, subject.identifier, title)
}

func (subject *entity) AttachSession(requestContext context.Context, identifier session.SessionID) error {
	return subject.owner.AttachSession(requestContext, subject.identifier, identifier)
}

func (subject *entity) InsertSessionBefore(
	requestContext context.Context,
	identifier session.SessionID,
	beforeIdentifier *session.SessionID,
) error {
	return subject.owner.InsertSessionBefore(requestContext, subject.identifier, identifier, beforeIdentifier)
}

func (subject *entity) DetachSession(requestContext context.Context, identifier session.SessionID) error {
	return subject.owner.DetachSession(requestContext, subject.identifier, identifier)
}

func (subject *entity) Status() Status {
	state, found := subject.owner.readState(subject.identifier)
	if !found {
		return StatusMissingDir
	}
	info, err := os.Stat(state.Path)
	if err != nil || !info.IsDir() {
		return StatusMissingDir
	}
	return StatusOK
}
