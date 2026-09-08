package workspaceapi

import (
	"errors"

	sessionframes "github.com/gorenx/goren/apiproxy/session/events"
)

// ChangedFrame upserts a complete workspace snapshot.
type ChangedFrame struct {
	Workspace View `json:"workspace"`
}

func (ChangedFrame) HostFrame()   {}
func (ChangedFrame) Type() string { return "host/workspace-changed" }
func (item ChangedFrame) Validate() error {
	return validateWorkspace(item.Workspace)
}

// RemovedFrame removes one workspace registration.
type RemovedFrame struct {
	WorkspaceID ID `json:"workspaceId"`
}

func (RemovedFrame) HostFrame()   {}
func (RemovedFrame) Type() string { return "host/workspace-removed" }
func (item RemovedFrame) Validate() error {
	if item.WorkspaceID == "" {
		return errors.New("workspaceId must be non-empty")
	}
	return nil
}

// OrderChangedFrame replaces the durable workspace order.
type OrderChangedFrame struct {
	WorkspaceIDs []ID `json:"workspaceIds"`
}

func (OrderChangedFrame) HostFrame()   {}
func (OrderChangedFrame) Type() string { return "host/workspace-order-changed" }
func (item OrderChangedFrame) Validate() error {
	if item.WorkspaceIDs == nil {
		return errors.New("workspaceIds must be an array")
	}
	for _, identifier := range item.WorkspaceIDs {
		if identifier == "" {
			return errors.New("workspaceId must be non-empty")
		}
	}
	return nil
}

// ArchivedSessionsChangedFrame replaces the durable archive set.
type ArchivedSessionsChangedFrame struct {
	ArchivedSessionIDs []sessionframes.SessionID `json:"archivedSessionIds"`
}

func (ArchivedSessionsChangedFrame) HostFrame()   {}
func (ArchivedSessionsChangedFrame) Type() string { return "host/archived-sessions-changed" }
func (item ArchivedSessionsChangedFrame) Validate() error {
	if item.ArchivedSessionIDs == nil {
		return errors.New("archivedSessionIds must be an array")
	}
	for _, identifier := range item.ArchivedSessionIDs {
		if err := sessionframes.ValidateSessionID(identifier); err != nil {
			return err
		}
	}
	return nil
}
