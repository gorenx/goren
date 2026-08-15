package sessionapi

import (
	"context"
	"errors"
	"fmt"

	api "github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
	sessiontitle "github.com/gorenx/goren/session/title"
	"github.com/gorenx/goren/workspace"
)

type sessionLifecycle struct {
	access           *sessionAccess
	titles           sessiontitle.TitleService
	workspaces       workspace.Registry
	workingDirectory string
	newSessionID     func() (session.SessionID, error)
}

func (flow *sessionLifecycle) Rename(requestContext context.Context, call api.Request[api.SessionRenameRequest]) (api.Outcome[api.SessionRenameValue], error) {
	subject, refused := flow.access.ordinaryAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		return api.Fail[api.SessionRenameValue](*refused), nil
	}
	accepted, err := flow.titles.Rename(subject.SessionValue(), call.Payload.Title)
	if err != nil {
		var invalid *sessiontitle.SessionTitleInvalidError
		if errors.As(err, &invalid) {
			return api.Fail[api.SessionRenameValue](api.NewRPCError(
				connection.ErrorTitleInvalid, invalid.Error(), struct {
					SessionID api.SessionID `json:"sessionId"`
				}{SessionID: call.Payload.SessionID},
			)), nil
		}
		return api.Fail[api.SessionRenameValue](api.NewRPCError(
			connection.ErrorInternal,
			fmt.Sprintf("failed to rename session %q: %v", call.Payload.SessionID, err),
			struct{}{},
		)), nil
	}
	return api.OK(api.SessionRenameValue{Title: accepted.Title, Seq: accepted.EventSeq}), nil
}

// Create publishes a fresh ordinary Agent or idempotently adopts the same id and cwd.
func (flow *sessionLifecycle) Create(requestContext context.Context, call api.Request[api.SessionCreateRequest]) (api.Outcome[api.SessionCreateValue], error) {
	var workspaceSubject workspace.Workspace
	var workspaceSnapshot workspace.WorkspaceState
	if call.Payload.WorkspaceID != nil {
		resolved, found := flow.workspaces.Get(workspace.ID(*call.Payload.WorkspaceID))
		if !found {
			return workspaceNotFound[api.SessionCreateValue](*call.Payload.WorkspaceID), nil
		}
		workspaceSnapshot = resolved.Snapshot()
		if workspaceSnapshot.ID == "" {
			return workspaceNotFound[api.SessionCreateValue](*call.Payload.WorkspaceID), nil
		}
		workspaceSubject = resolved
	}
	identifier := session.SessionID("")
	if call.Payload.SessionID == nil {
		generated, err := flow.newSessionID()
		if err != nil {
			return api.Outcome[api.SessionCreateValue]{}, err
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
	subject, err := flow.access.runtimeSessions.Ensure(
		requestContext, identifier, workingDirectory, call.Payload.AgentPreset, true, nil,
	)
	if err != nil {
		var ownership *SubagentOwnershipError
		if errors.As(err, &ownership) {
			return api.Fail[api.SessionCreateValue](subagentOwnershipError(api.SessionID(ownership.SessionID()))), nil
		}
		var presetConflict *PresetConflictError
		if errors.As(err, &presetConflict) {
			return api.Fail[api.SessionCreateValue](api.NewRPCError(
				connection.ErrorAgentPresetConflict, presetConflict.Error(), struct {
					SessionID       api.SessionID `json:"sessionId"`
					RequestedPreset string        `json:"requestedPreset"`
					ExistingPreset  *string       `json:"existingPreset,omitempty"`
				}{
					SessionID: api.SessionID(presetConflict.SessionID()), RequestedPreset: presetConflict.RequestedPreset(),
					ExistingPreset: presetConflict.ExistingPreset(),
				},
			)), nil
		}
		var conflict *CWDConflictError
		if errors.As(err, &conflict) {
			return api.Fail[api.SessionCreateValue](api.NewRPCError(
				connection.ErrorSessionConflict, conflict.Error(), struct {
					SessionID    api.SessionID `json:"sessionId"`
					RequestedCWD string        `json:"requestedCwd"`
					ExistingCWD  *string       `json:"existingCwd,omitempty"`
				}{
					SessionID: api.SessionID(conflict.SessionID()), RequestedCWD: conflict.RequestedCWD(),
					ExistingCWD: conflict.ExistingCWD(),
				},
			)), nil
		}
		return api.Fail[api.SessionCreateValue](api.NewRPCError(
			connection.ErrorInternal,
			fmt.Sprintf("failed to create session %q: %v", identifier, err),
			struct{}{},
		)), nil
	}
	if workspaceSubject != nil {
		if err := workspaceSubject.AttachSession(requestContext, subject.SessionValue().ID()); err != nil {
			return api.Fail[api.SessionCreateValue](api.NewRPCError(
				connection.ErrorWorkspaceAttachFailed,
				fmt.Sprintf(
					"session %q was created but could not attach to workspace %q: %v",
					subject.SessionValue().ID(), workspaceSnapshot.ID, err,
				),
				struct {
					SessionID   api.SessionID   `json:"sessionId"`
					WorkspaceID api.WorkspaceID `json:"workspaceId"`
				}{
					SessionID:   api.SessionID(subject.SessionValue().ID()),
					WorkspaceID: api.WorkspaceID(workspaceSnapshot.ID),
				},
			)), nil
		}
	}
	header := subject.SessionValue().Header()
	return api.OK(api.SessionCreateValue{
		SessionID: api.SessionID(identifier), AgentPreset: cloneStringPointer(header.AgentPreset),
	}), nil
}

func workspaceNotFound[V any](identifier api.WorkspaceID) api.Outcome[V] {
	return api.Fail[V](api.NewRPCError(
		connection.ErrorWorkspaceNotFound,
		"workspace \""+string(identifier)+"\" not found",
		struct {
			WorkspaceID api.WorkspaceID `json:"workspaceId"`
		}{WorkspaceID: identifier},
	))
}
