package sessionapi

import (
	"context"
	"errors"

	llmapi "github.com/gorenx/goren/apiproxy/llm"
	rpc "github.com/gorenx/goren/apiproxy/rpc"
	imageapi "github.com/gorenx/goren/apiproxy/session/image"
	"github.com/gorenx/goren/session"
)

// Dependencies are the domain capabilities consumed by the Session API.
type Dependencies struct {
	Agents      Agents
	Sessions    LiveSessions
	Persistence History
	Models      Models
	Titles      Titles
	Cache       ProjectionCache
	Workspaces  Workspaces
	Directories DirectoryProvisioner
	Storage     imageapi.ImageStore
}

// Options are process values and injectable identity sources.
type Options struct {
	WorkingDirectory  string
	NewSessionID      func() (session.SessionID, error)
	ReportReadFailure func(error)
}

// Gateway is the unary session.* facade registered with the wire catalog.
// Each capability object owns only its own dependencies and state.
type Gateway struct {
	pictures             *imageapi.Reader
	reader               *sessionReader
	lifecycle            *sessionLifecycle
	modelUseCases        *sessionModels
	conversationUseCases *sessionConversation
}

// NewGateway creates the typed unary Session API implementation.
func NewGateway(
	requestContext context.Context,
	ports Dependencies,
	settings Options,
) (*Gateway, error) {
	if requestContext == nil {
		return nil, errors.New("apiproxy/session: Gateway Context is required")
	}
	if ports.Agents == nil ||
		ports.Sessions == nil ||
		ports.Persistence == nil ||
		ports.Models == nil ||
		ports.Titles == nil ||
		ports.Workspaces == nil ||
		ports.Directories == nil {
		return nil, errors.New("session API: Gateway dependencies are incomplete")
	}
	if settings.WorkingDirectory == "" {
		return nil, errors.New("apiproxy/session: Gateway working directory is empty")
	}
	newSessionID := settings.NewSessionID
	if newSessionID == nil {
		newSessionID = mintSessionID
	}
	reportReadFailure := settings.ReportReadFailure
	if reportReadFailure == nil {
		reportReadFailure = func(error) {}
	}
	modelDirectory, err := llmapi.NewCatalog(ports.Models)
	if err != nil {
		return nil, err
	}
	access := &sessionAccess{
		runtimeSessions: &activation{
			agents:      ports.Agents,
			sessions:    ports.Sessions,
			history:     ports.Persistence,
			directories: ports.Directories,
		},
	}
	owner := &Gateway{
		reader: &sessionReader{
			sessions:      ports.Sessions,
			persistence:   ports.Persistence,
			cache:         ports.Cache,
			reportFailure: reportReadFailure,
		},
		lifecycle: &sessionLifecycle{
			access:           access,
			workspaces:       ports.Workspaces,
			sessions:         ports.Sessions,
			titles:           ports.Titles,
			workingDirectory: settings.WorkingDirectory,
			newSessionID:     newSessionID,
		},
		modelUseCases: &sessionModels{
			agents:    ports.Agents,
			history:   ports.Persistence,
			access:    access,
			models:    ports.Models,
			sessions:  ports.Sessions,
			directory: modelDirectory,
		},
		conversationUseCases: &sessionConversation{
			access:  access,
			models:  ports.Models,
			storage: ports.Storage,
		},
	}
	owner.pictures = imageapi.NewReader(owner.reader, ports.Storage)
	if err := requestContext.Err(); err != nil {
		return nil, err
	}
	return owner, nil
}

func (owner *Gateway) List(
	requestContext context.Context,
	call rpc.Request[ListRequest],
) (rpc.Outcome[ListValue], error) {
	return owner.reader.List(requestContext, call)
}

func (owner *Gateway) VisibleSessionIDs(
	requestContext context.Context,
) (map[session.SessionID]struct{}, error) {
	return owner.reader.VisibleSessionIDs(requestContext)
}

func (owner *Gateway) History(
	requestContext context.Context,
	call rpc.Request[HistoryRequest],
) (rpc.Outcome[HistoryValue], error) {
	return owner.reader.History(requestContext, call)
}

func (owner *Gateway) Rename(
	requestContext context.Context,
	call rpc.Request[RenameRequest],
) (rpc.Outcome[RenameValue], error) {
	return owner.lifecycle.Rename(requestContext, call)
}

func (owner *Gateway) Create(
	requestContext context.Context,
	call rpc.Request[CreateRequest],
) (rpc.Outcome[CreateValue], error) {
	return owner.lifecycle.Create(requestContext, call)
}

func (owner *Gateway) Models(
	requestContext context.Context,
	call rpc.Request[ModelsRequest],
) (rpc.Outcome[ModelsValue], error) {
	return owner.modelUseCases.Models(requestContext, call)
}

func (owner *Gateway) SelectModel(
	requestContext context.Context,
	call rpc.Request[SelectModelRequest],
) (rpc.Outcome[SelectModelValue], error) {
	return owner.modelUseCases.SelectModel(requestContext, call)
}

func (owner *Gateway) Prompt(
	requestContext context.Context,
	call rpc.Request[PromptRequest],
) (rpc.Outcome[PromptValue], error) {
	return owner.conversationUseCases.Prompt(requestContext, call)
}

func (owner *Gateway) UpdateQueue(
	requestContext context.Context,
	call rpc.Request[UpdateQueueRequest],
) (rpc.Outcome[AcceptedValue], error) {
	return owner.conversationUseCases.UpdateQueue(requestContext, call)
}

func (owner *Gateway) Cancel(
	requestContext context.Context,
	call rpc.Request[CancelRequest],
) (rpc.Outcome[AcceptedValue], error) {
	return owner.conversationUseCases.Cancel(requestContext, call)
}

// Attachment reads bytes only after the Session log authorizes the reference.
func (owner *Gateway) Attachment(ctx context.Context, call rpc.Request[imageapi.AttachmentRequest]) (rpc.Outcome[imageapi.AttachmentValue], error) {
	return owner.pictures.Attachment(ctx, call)
}
