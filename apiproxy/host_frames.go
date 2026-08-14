package apiproxy

import (
	"encoding/json"
	"errors"
	"fmt"
)

// HostFrame is the closed payload union for host-level changes.
type HostFrame interface {
	frameContract
	hostFrame()
}

// HostSessionAddedFrame announces a newly attached session.
type HostSessionAddedFrame struct {
	SessionID       SessionID `json:"sessionId"`
	Blank           bool      `json:"blank"`
	ParentSessionID SessionID `json:"parentSessionId,omitempty"`
	Origin          string    `json:"origin,omitempty"`
	CWD             *string   `json:"cwd,omitempty"`
	AgentPreset     *string   `json:"agentPreset,omitempty"`
}

func (HostSessionAddedFrame) hostFrame()        {}
func (HostSessionAddedFrame) frameType() string { return "host/session-added" }
func (item HostSessionAddedFrame) validate() error {
	if err := validateSessionIdentifier(item.SessionID); err != nil {
		return err
	}
	if item.ParentSessionID != "" {
		if err := validateSessionIdentifier(item.ParentSessionID); err != nil {
			return err
		}
	}
	if item.Origin != "" && item.Origin != "subagent" {
		return errors.New("session origin must be subagent")
	}
	return nil
}

// HostSessionRemovedFrame announces detachment of one session.
type HostSessionRemovedFrame struct {
	SessionID SessionID `json:"sessionId"`
}

func (HostSessionRemovedFrame) hostFrame()        {}
func (HostSessionRemovedFrame) frameType() string { return "host/session-removed" }
func (item HostSessionRemovedFrame) validate() error {
	return validateSessionIdentifier(item.SessionID)
}

// HostSessionStatusFrame publishes the running state of one session.
type HostSessionStatusFrame struct {
	SessionID SessionID `json:"sessionId"`
	Running   bool      `json:"running"`
}

func (HostSessionStatusFrame) hostFrame()        {}
func (HostSessionStatusFrame) frameType() string { return "host/session-status" }
func (item HostSessionStatusFrame) validate() error {
	return validateSessionIdentifier(item.SessionID)
}

// HostAgentErrorFrame reports a live Agent failure without turn position.
type HostAgentErrorFrame struct {
	SessionID SessionID `json:"sessionId"`
	Message   string    `json:"message"`
}

func (HostAgentErrorFrame) hostFrame()        {}
func (HostAgentErrorFrame) frameType() string { return "host/agent-error" }
func (item HostAgentErrorFrame) validate() error {
	return validateSessionIdentifier(item.SessionID)
}

// HostWorkspaceChangedFrame upserts a complete workspace snapshot.
type HostWorkspaceChangedFrame struct {
	Workspace WorkspaceView `json:"workspace"`
}

func (HostWorkspaceChangedFrame) hostFrame()        {}
func (HostWorkspaceChangedFrame) frameType() string { return "host/workspace-changed" }
func (item HostWorkspaceChangedFrame) validate() error {
	return validateWorkspace(item.Workspace)
}

// HostWorkspaceRemovedFrame removes one workspace registration.
type HostWorkspaceRemovedFrame struct {
	WorkspaceID WorkspaceID `json:"workspaceId"`
}

func (HostWorkspaceRemovedFrame) hostFrame()        {}
func (HostWorkspaceRemovedFrame) frameType() string { return "host/workspace-removed" }
func (item HostWorkspaceRemovedFrame) validate() error {
	if item.WorkspaceID == "" {
		return errors.New("workspaceId must be non-empty")
	}
	return nil
}

// HostWorkspaceOrderChangedFrame replaces the durable workspace order.
type HostWorkspaceOrderChangedFrame struct {
	WorkspaceIDs []WorkspaceID `json:"workspaceIds"`
}

func (HostWorkspaceOrderChangedFrame) hostFrame()        {}
func (HostWorkspaceOrderChangedFrame) frameType() string { return "host/workspace-order-changed" }
func (item HostWorkspaceOrderChangedFrame) validate() error {
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

// HostArchivedSessionsChangedFrame replaces the durable archive set.
type HostArchivedSessionsChangedFrame struct {
	ArchivedSessionIDs []SessionID `json:"archivedSessionIds"`
}

func (HostArchivedSessionsChangedFrame) hostFrame()        {}
func (HostArchivedSessionsChangedFrame) frameType() string { return "host/archived-sessions-changed" }
func (item HostArchivedSessionsChangedFrame) validate() error {
	if item.ArchivedSessionIDs == nil {
		return errors.New("archivedSessionIds must be an array")
	}
	for _, identifier := range item.ArchivedSessionIDs {
		if err := validateSessionIdentifier(identifier); err != nil {
			return err
		}
	}
	return nil
}

// HostRemoteEventFrame forwards one allowlisted Host event. Args retain the
// event owner's JSON value contract.
type HostRemoteEventFrame struct {
	Event string            `json:"event"`
	Args  []json.RawMessage `json:"args"`
}

func (HostRemoteEventFrame) hostFrame()        {}
func (HostRemoteEventFrame) frameType() string { return "host/remote-event" }
func (item HostRemoteEventFrame) validate() error {
	if item.Event == "" {
		return errors.New("remote event name must be non-empty")
	}
	if item.Args == nil {
		return errors.New("remote event args must be an array")
	}
	for index, argument := range item.Args {
		if argument != nil && !json.Valid(argument) {
			return fmt.Errorf("remote event arg %d must be JSON", index)
		}
	}
	return nil
}

// EncodeHostFrame validates and serializes one Host payload with its canonical
// type discriminant.
func EncodeHostFrame(payload HostFrame) (json.RawMessage, error) {
	encoded, err := encodeFrame(payload)
	if err != nil {
		return nil, fmt.Errorf("apiproxy: encode host frame: %w", err)
	}
	return encoded, nil
}
