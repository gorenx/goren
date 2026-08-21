package sessionapi

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentdefaultmodel"
	api "github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
	sessiontitle "github.com/gorenx/goren/session/title"
	"github.com/gorenx/goren/workspace"
)

// Dependencies are the domain capabilities consumed by the Session API.
type Dependencies struct {
	Agents      agent.Registry
	Sessions    session.LiveStore
	Persistence sesspersist.Persistence
	LLM         llm.LlmRuntime
	Defaults    agentdefaultmodel.DefaultModel
	Projections sessionprojection.Registry
	Titles      sessiontitle.TitleService
	Workspaces  workspace.Registry
	Directories DirectoryProvisioner
}

// Options are process values and injectable identity sources.
type Options struct {
	WorkingDirectory string
	NewSessionID     func() (session.SessionID, error)
}

// Gateway is the unary session.* facade registered with the wire catalog.
// Each capability object owns only its own dependencies and state.
type Gateway struct {
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
	if ports.Agents == nil || ports.Sessions == nil || ports.Persistence == nil || ports.LLM == nil ||
		ports.Defaults == nil || ports.Projections == nil || ports.Titles == nil ||
		ports.Workspaces == nil || ports.Directories == nil {
		return nil, errors.New("apiproxy/session: Gateway dependencies are incomplete")
	}
	if settings.WorkingDirectory == "" {
		return nil, errors.New("apiproxy/session: Gateway working directory is empty")
	}
	newSessionID := settings.NewSessionID
	if newSessionID == nil {
		newSessionID = mintSessionID
	}
	modelDirectory, err := api.NewLLMGateway(ports.LLM)
	if err != nil {
		return nil, err
	}
	runtimeSessions, err := NewAgentSessions(AgentSessionDependencies{
		Agents:      ports.Agents,
		Sessions:    ports.Sessions,
		Persistence: ports.Persistence,
		Defaults:    ports.Defaults,
		Directories: ports.Directories,
	})
	if err != nil {
		return nil, err
	}
	access := &sessionAccess{runtimeSessions: runtimeSessions}
	owner := &Gateway{
		reader: &sessionReader{
			agents:      ports.Agents,
			sessions:    ports.Sessions,
			persistence: ports.Persistence,
			projections: ports.Projections,
		},
		lifecycle: &sessionLifecycle{
			access:           access,
			titles:           ports.Titles,
			workspaces:       ports.Workspaces,
			workingDirectory: settings.WorkingDirectory,
			newSessionID:     newSessionID,
		},
		modelUseCases: &sessionModels{
			access:    access,
			runtime:   ports.LLM,
			directory: modelDirectory,
			defaults:  ports.Defaults,
		},
		conversationUseCases: &sessionConversation{
			access:  access,
			runtime: ports.LLM,
		},
	}
	if err := requestContext.Err(); err != nil {
		return nil, err
	}
	return owner, nil
}

func (owner *Gateway) List(
	requestContext context.Context,
	call api.Request[api.SessionListRequest],
) (api.Outcome[api.SessionListValue], error) {
	return owner.reader.List(requestContext, call)
}

func (owner *Gateway) VisibleSessionIDs(
	requestContext context.Context,
) (map[session.SessionID]struct{}, error) {
	return owner.reader.VisibleSessionIDs(requestContext)
}

func (owner *Gateway) History(
	requestContext context.Context,
	call api.Request[api.SessionHistoryRequest],
) (api.Outcome[api.SessionHistoryValue], error) {
	return owner.reader.History(requestContext, call)
}

func (owner *Gateway) Rename(
	requestContext context.Context,
	call api.Request[api.SessionRenameRequest],
) (api.Outcome[api.SessionRenameValue], error) {
	return owner.lifecycle.Rename(requestContext, call)
}

func (owner *Gateway) Create(
	requestContext context.Context,
	call api.Request[api.SessionCreateRequest],
) (api.Outcome[api.SessionCreateValue], error) {
	return owner.lifecycle.Create(requestContext, call)
}

func (owner *Gateway) Models(
	requestContext context.Context,
	call api.Request[api.SessionModelsRequest],
) (api.Outcome[api.SessionModelsValue], error) {
	return owner.modelUseCases.Models(requestContext, call)
}

func (owner *Gateway) SelectModel(
	requestContext context.Context,
	call api.Request[api.SessionSelectModelRequest],
) (api.Outcome[api.SessionSelectModelValue], error) {
	return owner.modelUseCases.SelectModel(requestContext, call)
}

func (owner *Gateway) Prompt(
	requestContext context.Context,
	call api.Request[api.SessionPromptRequest],
) (api.Outcome[api.SessionPromptValue], error) {
	return owner.conversationUseCases.Prompt(requestContext, call)
}

func (owner *Gateway) UpdateQueue(
	requestContext context.Context,
	call api.Request[api.SessionUpdateQueueRequest],
) (api.Outcome[api.AcceptedValue], error) {
	return owner.conversationUseCases.UpdateQueue(requestContext, call)
}

func (owner *Gateway) Cancel(
	requestContext context.Context,
	call api.Request[api.SessionCancelRequest],
) (api.Outcome[api.AcceptedValue], error) {
	return owner.conversationUseCases.Cancel(requestContext, call)
}
