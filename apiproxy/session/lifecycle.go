package sessionapi

import (
	"context"
	"errors"
	"fmt"

	rpc "github.com/gorenx/goren/apiproxy/rpc"
	sessionframes "github.com/gorenx/goren/apiproxy/session/events"
	workspaceapi "github.com/gorenx/goren/apiproxy/workspace"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
	sessiontitle "github.com/gorenx/goren/session/title"
	"github.com/gorenx/goren/workspace"
)

type sessionLifecycle struct {
	access           *sessionAccess
	workspaces       Workspaces
	workingDirectory string
	newSessionID     func() (session.SessionID, error)
}

func (flow *sessionLifecycle) Rename(requestContext context.Context, call rpc.Request[RenameRequest]) (rpc.Outcome[RenameValue], error) {
	subject, refused := flow.access.ordinaryAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		return rpc.Fail[RenameValue](*refused), nil
	}
	accepted, err := subject.Rename(requestContext, call.Payload.Title)
	if err != nil {
		var invalid *sessiontitle.SessionTitleInvalidError
		if errors.As(err, &invalid) {
			return rpc.Fail[RenameValue](rpc.NewRPCError(
				connection.ErrorTitleInvalid, invalid.Error(), struct {
					SessionID sessionframes.SessionID `json:"sessionId"`
				}{
					SessionID: call.Payload.SessionID,
				},
			)), nil
		}
		return rpc.Fail[RenameValue](rpc.NewRPCError(
			connection.ErrorInternal,
			fmt.Sprintf("failed to rename session %q: %v", call.Payload.SessionID, err),
			struct{}{},
		)), nil
	}
	return rpc.OK(RenameValue{
		Title: accepted.Text,
		Seq:   accepted.Seq,
	}), nil
}

// Create publishes a fresh ordinary Agent or idempotently adopts the same id and cwd.
func (flow *sessionLifecycle) Create(requestContext context.Context, call rpc.Request[CreateRequest]) (rpc.Outcome[CreateValue], error) {
	var workspaceSubject *workspace.WorkspaceState
	var workspaceSnapshot workspace.WorkspaceState
	if call.Payload.WorkspaceID != nil {
		resolved, err := flow.workspaces.Read(requestContext, workspace.ID(*call.Payload.WorkspaceID))
		if err != nil {
			return rpc.Outcome[CreateValue]{}, err
		}
		if resolved == nil {
			return workspaceNotFound[CreateValue](*call.Payload.WorkspaceID), nil
		}
		workspaceSnapshot = *resolved
		if workspaceSnapshot.ID == "" {
			return workspaceNotFound[CreateValue](*call.Payload.WorkspaceID), nil
		}
		workspaceSubject = resolved
	}
	identifier := session.SessionID("")
	if call.Payload.SessionID == nil {
		generated, err := flow.newSessionID()
		if err != nil {
			return rpc.Outcome[CreateValue]{}, err
		}
		identifier = generated
	} else {
		identifier = session.SessionID(*call.Payload.SessionID)
	}
	workingDirectory := flow.workingDirectory
	if workspaceSubject != nil {
		workingDirectory = workspaceSnapshot.Path
	} else if call.Payload.CWD != nil {
		workingDirectory = *call.Payload.CWD
	}
	subject, err := flow.access.runtimeSessions.ensure(
		requestContext, identifier, workingDirectory, call.Payload.AgentPreset,
	)
	if err != nil {
		var ownership *SubagentOwnershipError
		if errors.As(err, &ownership) {
			return rpc.Fail[CreateValue](subagentOwnershipError(sessionframes.SessionID(ownership.SessionID()))), nil
		}
		var presetConflict *PresetConflictError
		if errors.As(err, &presetConflict) {
			return rpc.Fail[CreateValue](rpc.NewRPCError(
				connection.ErrorAgentPresetConflict, presetConflict.Error(), struct {
					SessionID       sessionframes.SessionID `json:"sessionId"`
					RequestedPreset string                  `json:"requestedPreset"`
					ExistingPreset  *string                 `json:"existingPreset,omitempty"`
				}{
					SessionID:       sessionframes.SessionID(presetConflict.SessionID()),
					RequestedPreset: presetConflict.RequestedPreset(),
					ExistingPreset:  presetConflict.ExistingPreset(),
				},
			)), nil
		}
		var conflict *CWDConflictError
		if errors.As(err, &conflict) {
			return rpc.Fail[CreateValue](rpc.NewRPCError(
				connection.ErrorSessionConflict, conflict.Error(), struct {
					SessionID    sessionframes.SessionID `json:"sessionId"`
					RequestedCWD string                  `json:"requestedCwd"`
					ExistingCWD  *string                 `json:"existingCwd,omitempty"`
				}{
					SessionID:    sessionframes.SessionID(conflict.SessionID()),
					RequestedCWD: conflict.RequestedCWD(),
					ExistingCWD:  conflict.ExistingCWD(),
				},
			)), nil
		}
		return rpc.Fail[CreateValue](rpc.NewRPCError(
			connection.ErrorInternal,
			fmt.Sprintf("failed to create session %q: %v", identifier, err),
			struct{}{},
		)), nil
	}
	if workspaceSubject != nil {
		if err := flow.workspaces.AttachSession(requestContext, workspaceSnapshot.ID, subject.ID()); err != nil {
			return rpc.Fail[CreateValue](rpc.NewRPCError(
				connection.ErrorWorkspaceAttachFailed,
				fmt.Sprintf(
					"session %q was created but could not attach to workspace %q: %v",
					subject.ID(), workspaceSnapshot.ID, err,
				),
				struct {
					SessionID   sessionframes.SessionID `json:"sessionId"`
					WorkspaceID workspaceapi.ID         `json:"workspaceId"`
				}{
					SessionID:   sessionframes.SessionID(subject.ID()),
					WorkspaceID: workspaceapi.ID(workspaceSnapshot.ID),
				},
			)), nil
		}
	}
	header := subject.Header()
	return rpc.OK(CreateValue{
		SessionID:   sessionframes.SessionID(identifier),
		AgentPreset: cloneStringPointer(header.AgentPreset),
	}), nil
}

func workspaceNotFound[V any](identifier workspaceapi.ID) rpc.Outcome[V] {
	return rpc.Fail[V](rpc.NewRPCError(
		connection.ErrorWorkspaceNotFound,
		"workspace \""+string(identifier)+"\" not found",
		struct {
			WorkspaceID workspaceapi.ID `json:"workspaceId"`
		}{
			WorkspaceID: identifier,
		},
	))
}
