package apiproxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/workspace"
)

// HostFramePublisher is the Workspace Gateway's consumer-owned downlink port.
type HostFramePublisher interface {
	PublishHostFrame(HostFrame) error
}

// WorkspaceGateway maps the Workspace domain to the pinned workspace.* API.
type WorkspaceGateway struct {
	registry  workspace.Registry
	publisher HostFramePublisher
	mutation  sync.Mutex
}

// NewWorkspaceGateway constructs the Workspace API adapter. The owning API
// Proxy Plugin declares and routes post-commit Workspace Events.
func NewWorkspaceGateway(
	registry workspace.Registry,
	publisher HostFramePublisher,
) (*WorkspaceGateway, error) {
	if registry == nil || publisher == nil {
		return nil, errors.New("apiproxy: Workspace Gateway dependencies are incomplete")
	}
	return &WorkspaceGateway{
		registry:  registry,
		publisher: publisher,
	}, nil
}

// ObserveEvent projects the Workspace Events declared by the owning API Proxy
// Plugin.
func (owner *WorkspaceGateway) ObserveEvent(
	requestContext context.Context,
	fact plugin.Event,
) error {
	switch observed := fact.(type) {
	case workspace.ChangedNotice:
		return owner.observeChanged(requestContext, observed.WorkspaceState)
	case workspace.RemovedNotice:
		return owner.observeRemoved(requestContext, observed.ID)
	case workspace.OrderChangedNotice:
		return owner.observeOrderChanged(requestContext, observed.WorkspaceIDs)
	case workspace.ArchivedSessionsChangedNotice:
		return owner.observeArchivedChanged(requestContext, observed.SessionIDs)
	default:
		return nil
	}
}

func (owner *WorkspaceGateway) List(
	_ context.Context,
	_ Request[WorkspaceListRequest],
) (Outcome[WorkspaceListValue], error) {
	items := owner.registry.List()
	views := make([]WorkspaceView, 0, len(items))
	for _, subject := range items {
		views = append(views, projectWorkspace(subject.Snapshot()))
	}
	archived := owner.registry.ArchivedSessionIDs()
	archivedIDs := make([]SessionID, len(archived))
	for index, identifier := range archived {
		archivedIDs[index] = SessionID(identifier)
	}
	return OK(WorkspaceListValue{Items: views, ArchivedSessionIDs: archivedIDs}), nil
}

func (owner *WorkspaceGateway) Create(
	requestContext context.Context,
	call Request[WorkspaceCreateRequest],
) (Outcome[WorkspaceCreateValue], error) {
	owner.mutation.Lock()
	defer owner.mutation.Unlock()
	subject, created, err := owner.registry.Create(requestContext, call.Payload.Path)
	if err != nil {
		return Fail[WorkspaceCreateValue](NewRPCError(
			connection.ErrorWorkspaceInvalidPath,
			fmt.Sprintf("cannot create a workspace at %q: %v", call.Payload.Path, err),
			struct {
				Path string `json:"path"`
			}{Path: call.Payload.Path},
		)), nil
	}
	return OK(WorkspaceCreateValue{Workspace: projectWorkspace(subject.Snapshot()), Created: created}), nil
}

func (owner *WorkspaceGateway) Rename(
	requestContext context.Context,
	call Request[WorkspaceRenameRequest],
) (Outcome[WorkspaceRenameValue], error) {
	owner.mutation.Lock()
	defer owner.mutation.Unlock()
	identifier := workspace.ID(call.Payload.WorkspaceID)
	subject, found := owner.registry.Get(identifier)
	if !found {
		return workspaceNotFound[WorkspaceRenameValue](call.Payload.WorkspaceID), nil
	}
	title := strings.TrimSpace(call.Payload.Title)
	current := subject.Snapshot()
	if current.Title != title {
		for _, candidate := range owner.registry.List() {
			candidateState := candidate.Snapshot()
			if candidateState.ID != identifier && candidateState.Title == title {
				return Fail[WorkspaceRenameValue](NewRPCError(
					connection.ErrorWorkspaceNameConflict,
					fmt.Sprintf("workspace name %q is already in use", title),
					struct {
						Name string `json:"name"`
					}{Name: title},
				)), nil
			}
		}
		if err := subject.SetTitle(requestContext, title); err != nil {
			var unknown *workspace.UnknownError
			if errors.As(err, &unknown) {
				return workspaceNotFound[WorkspaceRenameValue](call.Payload.WorkspaceID), nil
			}
			return Outcome[WorkspaceRenameValue]{}, err
		}
	}
	return OK(WorkspaceRenameValue{Workspace: projectWorkspace(subject.Snapshot())}), nil
}

func (owner *WorkspaceGateway) Delete(
	requestContext context.Context,
	call Request[WorkspaceDeleteRequest],
) (Outcome[WorkspaceDeleteValue], error) {
	owner.mutation.Lock()
	defer owner.mutation.Unlock()
	deleted, err := owner.registry.Delete(requestContext, workspace.ID(call.Payload.WorkspaceID))
	if err != nil {
		return Outcome[WorkspaceDeleteValue]{}, err
	}
	if !deleted {
		return workspaceNotFound[WorkspaceDeleteValue](call.Payload.WorkspaceID), nil
	}
	return OK(WorkspaceDeleteValue{Deleted: true}), nil
}

func (owner *WorkspaceGateway) InsertBefore(
	requestContext context.Context,
	call Request[WorkspaceInsertBeforeRequest],
) (Outcome[WorkspaceInsertBeforeValue], error) {
	var beforeIdentifier *workspace.ID
	if call.Payload.BeforeWorkspaceID != nil {
		converted := workspace.ID(*call.Payload.BeforeWorkspaceID)
		beforeIdentifier = &converted
	}
	identifiers, err := owner.registry.InsertBefore(
		requestContext, workspace.ID(call.Payload.WorkspaceID), beforeIdentifier,
	)
	if err != nil {
		var invalid *workspace.OrderInvalidError
		if errors.As(err, &invalid) {
			return workspaceNotFound[WorkspaceInsertBeforeValue](WorkspaceID(invalid.ID)), nil
		}
		return Outcome[WorkspaceInsertBeforeValue]{}, err
	}
	workspaceIDs := make([]WorkspaceID, len(identifiers))
	for index, identifier := range identifiers {
		workspaceIDs[index] = WorkspaceID(identifier)
	}
	return OK(WorkspaceInsertBeforeValue{WorkspaceIDs: workspaceIDs}), nil
}

func (owner *WorkspaceGateway) InsertSessionBefore(
	requestContext context.Context,
	call Request[WorkspaceInsertSessionBeforeRequest],
) (Outcome[WorkspaceInsertSessionBeforeValue], error) {
	subject, found := owner.registry.Get(workspace.ID(call.Payload.WorkspaceID))
	if !found {
		return workspaceNotFound[WorkspaceInsertSessionBeforeValue](call.Payload.WorkspaceID), nil
	}
	var beforeIdentifier *session.SessionID
	if call.Payload.BeforeSessionID != nil {
		converted := session.SessionID(*call.Payload.BeforeSessionID)
		beforeIdentifier = &converted
	}
	err := subject.InsertSessionBefore(
		requestContext, session.SessionID(call.Payload.SessionID), beforeIdentifier,
	)
	if err != nil {
		var invalid *workspace.MoveInvalidError
		if errors.As(err, &invalid) {
			return Fail[WorkspaceInsertSessionBeforeValue](NewRPCError(
				connection.ErrorWorkspaceMoveInvalid, invalid.Error(), struct {
					WorkspaceID     WorkspaceID `json:"workspaceId"`
					SessionID       SessionID   `json:"sessionId"`
					BeforeSessionID *SessionID  `json:"beforeSessionId,omitempty"`
				}{
					WorkspaceID: call.Payload.WorkspaceID, SessionID: call.Payload.SessionID,
					BeforeSessionID: cloneSessionWireID(call.Payload.BeforeSessionID),
				},
			)), nil
		}
		var unknown *workspace.UnknownError
		if errors.As(err, &unknown) {
			return workspaceNotFound[WorkspaceInsertSessionBeforeValue](call.Payload.WorkspaceID), nil
		}
		return Outcome[WorkspaceInsertSessionBeforeValue]{}, err
	}
	return OK(WorkspaceInsertSessionBeforeValue{Workspace: projectWorkspace(subject.Snapshot())}), nil
}

func (owner *WorkspaceGateway) ArchiveSession(
	requestContext context.Context,
	call Request[WorkspaceArchiveSessionRequest],
) (Outcome[WorkspaceArchiveSessionValue], error) {
	if err := owner.registry.ArchiveSession(requestContext, session.SessionID(call.Payload.SessionID)); err != nil {
		var unknown *workspace.UnknownSessionError
		if errors.As(err, &unknown) {
			return Fail[WorkspaceArchiveSessionValue](NewRPCError(
				connection.ErrorSessionNotFound, unknown.Error(), struct {
					SessionID SessionID `json:"sessionId"`
				}{SessionID: call.Payload.SessionID},
			)), nil
		}
		return Outcome[WorkspaceArchiveSessionValue]{}, err
	}
	archived := owner.registry.ArchivedSessionIDs()
	identifiers := make([]SessionID, len(archived))
	for index, identifier := range archived {
		identifiers[index] = SessionID(identifier)
	}
	return OK(WorkspaceArchiveSessionValue{ArchivedSessionIDs: identifiers}), nil
}

func (owner *WorkspaceGateway) observeChanged(_ context.Context, state workspace.WorkspaceState) error {
	return owner.publisher.PublishHostFrame(HostWorkspaceChangedFrame{Workspace: projectWorkspace(state)})
}

func (owner *WorkspaceGateway) observeRemoved(_ context.Context, identifier workspace.ID) error {
	return owner.publisher.PublishHostFrame(HostWorkspaceRemovedFrame{WorkspaceID: WorkspaceID(identifier)})
}

func (owner *WorkspaceGateway) observeOrderChanged(_ context.Context, identifiers []workspace.ID) error {
	workspaceIDs := make([]WorkspaceID, len(identifiers))
	for index, identifier := range identifiers {
		workspaceIDs[index] = WorkspaceID(identifier)
	}
	return owner.publisher.PublishHostFrame(HostWorkspaceOrderChangedFrame{WorkspaceIDs: workspaceIDs})
}

func (owner *WorkspaceGateway) observeArchivedChanged(
	_ context.Context,
	identifiers []session.SessionID,
) error {
	archived := make([]SessionID, len(identifiers))
	for index, identifier := range identifiers {
		archived[index] = SessionID(identifier)
	}
	return owner.publisher.PublishHostFrame(HostArchivedSessionsChangedFrame{ArchivedSessionIDs: archived})
}

func projectWorkspace(state workspace.WorkspaceState) WorkspaceView {
	sessionIDs := make([]SessionID, len(state.SessionIDs))
	for index, identifier := range state.SessionIDs {
		sessionIDs[index] = SessionID(identifier)
	}
	return WorkspaceView{
		WorkspaceID: WorkspaceID(state.ID), Path: state.Path, Title: state.Title,
		SessionIDs: sessionIDs,
		CreatedAt:  state.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:  state.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func cloneSessionWireID(source *SessionID) *SessionID {
	if source == nil {
		return nil
	}
	result := *source
	return &result
}
