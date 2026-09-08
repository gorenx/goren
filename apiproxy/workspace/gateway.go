package workspaceapi

import (
	"context"
	"errors"
	"fmt"

	sessionframes "github.com/gorenx/goren/apiproxy/session/events"

	"time"

	rpc "github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/apiproxy/stream"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/workspace"
)

// Publisher is the Workspace Gateway's consumer-owned downlink port.
type Publisher interface {
	PublishHostFrame(stream.HostFrame) error
}

// Gateway maps the Workspace domain to the pinned workspace.* API.
type Gateway struct {
	registry  Directory
	publisher Publisher
}

// NewGateway connects Workspace operations and the Host downlink.
func NewGateway(
	registry Directory,
	publisher Publisher,
) (*Gateway, error) {
	if registry == nil || publisher == nil {
		return nil, errors.New("apiproxy: Workspace Gateway dependencies are incomplete")
	}
	return &Gateway{
		registry:  registry,
		publisher: publisher,
	}, nil
}

// ObserveEvent projects committed Workspace notices into Host notifications.
func (owner *Gateway) ObserveEvent(
	requestContext context.Context,
	fact workspace.Notice,
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

func (owner *Gateway) List(
	requestContext context.Context,
	_ rpc.Request[ListRequest],
) (rpc.Outcome[ListValue], error) {
	listing, err := owner.registry.List(requestContext)
	if err != nil {
		return rpc.Outcome[ListValue]{}, err
	}
	items := listing.Workspaces
	views := make([]View, 0, len(items))
	for _, subject := range items {
		views = append(views, projectWorkspace(subject))
	}
	archived := listing.ArchivedSessionIDs
	archivedIDs := make([]sessionframes.SessionID, len(archived))
	for index, identifier := range archived {
		archivedIDs[index] = sessionframes.SessionID(identifier)
	}
	return rpc.OK(ListValue{
		Items:              views,
		ArchivedSessionIDs: archivedIDs,
	}), nil
}

func (owner *Gateway) Create(
	requestContext context.Context,
	call rpc.Request[CreateRequest],
) (rpc.Outcome[CreateValue], error) {
	subject, created, err := owner.registry.Create(requestContext, call.Payload.Path)
	if err != nil {
		return rpc.Fail[CreateValue](rpc.NewRPCError(
			connection.ErrorWorkspaceInvalidPath,
			fmt.Sprintf("cannot create a workspace at %q: %v", call.Payload.Path, err),
			struct {
				Path string `json:"path"`
			}{
				Path: call.Payload.Path,
			},
		)), nil
	}
	return rpc.OK(CreateValue{
		Workspace: projectWorkspace(subject),
		Created:   created,
	}), nil
}

func (owner *Gateway) Rename(
	requestContext context.Context,
	call rpc.Request[RenameRequest],
) (rpc.Outcome[RenameValue], error) {
	state, err := owner.registry.Rename(requestContext, workspace.ID(call.Payload.WorkspaceID), call.Payload.Title)
	if err != nil {
		var unknown *workspace.UnknownError
		if errors.As(err, &unknown) {
			return workspaceNotFound[RenameValue](call.Payload.WorkspaceID), nil
		}
		var conflict *workspace.NameConflictError
		if errors.As(err, &conflict) {
			return rpc.Fail[RenameValue](rpc.NewRPCError(
				connection.ErrorWorkspaceNameConflict, conflict.Error(), struct {
					Name string `json:"name"`
				}{
					Name: conflict.Title,
				},
			)), nil
		}
		return rpc.Outcome[RenameValue]{}, err
	}
	return rpc.OK(RenameValue{
		Workspace: projectWorkspace(state),
	}), nil
}

func (owner *Gateway) Delete(
	requestContext context.Context,
	call rpc.Request[DeleteRequest],
) (rpc.Outcome[DeleteValue], error) {
	deleted, err := owner.registry.Delete(requestContext, workspace.ID(call.Payload.WorkspaceID))
	if err != nil {
		return rpc.Outcome[DeleteValue]{}, err
	}
	if !deleted {
		return workspaceNotFound[DeleteValue](call.Payload.WorkspaceID), nil
	}
	return rpc.OK(DeleteValue{
		Deleted: true,
	}), nil
}

func (owner *Gateway) InsertBefore(
	requestContext context.Context,
	call rpc.Request[InsertBeforeRequest],
) (rpc.Outcome[InsertBeforeValue], error) {
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
			return workspaceNotFound[InsertBeforeValue](ID(invalid.ID)), nil
		}
		return rpc.Outcome[InsertBeforeValue]{}, err
	}
	workspaceIDs := make([]ID, len(identifiers))
	for index, identifier := range identifiers {
		workspaceIDs[index] = ID(identifier)
	}
	return rpc.OK(InsertBeforeValue{
		WorkspaceIDs: workspaceIDs,
	}), nil
}

func (owner *Gateway) InsertSessionBefore(
	requestContext context.Context,
	call rpc.Request[InsertSessionBeforeRequest],
) (rpc.Outcome[InsertSessionBeforeValue], error) {
	var beforeIdentifier *session.SessionID
	if call.Payload.BeforeSessionID != nil {
		converted := session.SessionID(*call.Payload.BeforeSessionID)
		beforeIdentifier = &converted
	}
	subject, err := owner.registry.InsertSessionBefore(
		requestContext, workspace.ID(call.Payload.WorkspaceID), session.SessionID(call.Payload.SessionID), beforeIdentifier,
	)
	if err != nil {
		var invalid *workspace.MoveInvalidError
		if errors.As(err, &invalid) {
			return rpc.Fail[InsertSessionBeforeValue](rpc.NewRPCError(
				connection.ErrorWorkspaceMoveInvalid, invalid.Error(), struct {
					WorkspaceID     ID                       `json:"workspaceId"`
					SessionID       sessionframes.SessionID  `json:"sessionId"`
					BeforeSessionID *sessionframes.SessionID `json:"beforeSessionId,omitempty"`
				}{
					WorkspaceID:     call.Payload.WorkspaceID,
					SessionID:       call.Payload.SessionID,
					BeforeSessionID: cloneSessionWireID(call.Payload.BeforeSessionID),
				},
			)), nil
		}
		var unknown *workspace.UnknownError
		if errors.As(err, &unknown) {
			return workspaceNotFound[InsertSessionBeforeValue](call.Payload.WorkspaceID), nil
		}
		return rpc.Outcome[InsertSessionBeforeValue]{}, err
	}
	return rpc.OK(InsertSessionBeforeValue{
		Workspace: projectWorkspace(subject),
	}), nil
}

func (owner *Gateway) ArchiveSession(
	requestContext context.Context,
	call rpc.Request[ArchiveSessionRequest],
) (rpc.Outcome[ArchiveSessionValue], error) {
	archived, err := owner.registry.ArchiveSession(requestContext, session.SessionID(call.Payload.SessionID))
	if err != nil {
		var unknown *workspace.UnknownSessionError
		if errors.As(err, &unknown) {
			return rpc.Fail[ArchiveSessionValue](rpc.NewRPCError(
				connection.ErrorSessionNotFound, unknown.Error(), struct {
					SessionID sessionframes.SessionID `json:"sessionId"`
				}{
					SessionID: call.Payload.SessionID,
				},
			)), nil
		}
		return rpc.Outcome[ArchiveSessionValue]{}, err
	}
	identifiers := make([]sessionframes.SessionID, len(archived))
	for index, identifier := range archived {
		identifiers[index] = sessionframes.SessionID(identifier)
	}
	return rpc.OK(ArchiveSessionValue{
		ArchivedSessionIDs: identifiers,
	}), nil
}

func (owner *Gateway) observeChanged(_ context.Context, state workspace.WorkspaceState) error {
	return owner.publisher.PublishHostFrame(ChangedFrame{
		Workspace: projectWorkspace(state),
	})
}

func (owner *Gateway) observeRemoved(_ context.Context, identifier workspace.ID) error {
	return owner.publisher.PublishHostFrame(RemovedFrame{
		WorkspaceID: ID(identifier),
	})
}

func (owner *Gateway) observeOrderChanged(_ context.Context, identifiers []workspace.ID) error {
	workspaceIDs := make([]ID, len(identifiers))
	for index, identifier := range identifiers {
		workspaceIDs[index] = ID(identifier)
	}
	return owner.publisher.PublishHostFrame(OrderChangedFrame{
		WorkspaceIDs: workspaceIDs,
	})
}

func (owner *Gateway) observeArchivedChanged(
	_ context.Context,
	identifiers []session.SessionID,
) error {
	archived := make([]sessionframes.SessionID, len(identifiers))
	for index, identifier := range identifiers {
		archived[index] = sessionframes.SessionID(identifier)
	}
	return owner.publisher.PublishHostFrame(ArchivedSessionsChangedFrame{
		ArchivedSessionIDs: archived,
	})
}

func projectWorkspace(state workspace.WorkspaceState) View {
	sessionIDs := make([]sessionframes.SessionID, len(state.SessionIDs))
	for index, identifier := range state.SessionIDs {
		sessionIDs[index] = sessionframes.SessionID(identifier)
	}
	return View{
		WorkspaceID: ID(state.ID),
		Path:        state.Path,
		Title:       state.Title,
		SessionIDs:  sessionIDs,
		CreatedAt:   state.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:   state.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func cloneSessionWireID(source *sessionframes.SessionID) *sessionframes.SessionID {
	if source == nil {
		return nil
	}
	result := *source
	return &result
}
