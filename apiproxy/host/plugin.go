// Package host composes the API Proxy's typed gateways into the canonical
// runtime Plugin.
package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentdefaultmodel"
	"github.com/gorenx/goren/apiproxy"
	sessionapi "github.com/gorenx/goren/apiproxy/session"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/credentials"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
	sessionquery "github.com/gorenx/goren/session/query"
	sessiontitle "github.com/gorenx/goren/session/title"
	"github.com/gorenx/goren/userquestions"
	"github.com/gorenx/goren/workspace"
)

// Settings is the validated deployment configuration used by the API Proxy.
type Settings struct {
	Version string
}

// RuntimeOptions supplies process-owned values and technical collaborators.
type RuntimeOptions struct {
	WorkingDirectory string
	EnsureDirectory  func(string) error
	NewRPCID         func() (connection.RPCID, error)
	NewSessionID     func() (session.SessionID, error)
	ObserverError    func(error)
}

// Plugin owns the API method Catalog, Session use cases, live frames,
// Workspace projection, and interactive response correlation.
type Plugin struct {
	plugin.Base
	deploymentSettings Settings
	options            RuntimeOptions

	methods           *apiproxy.Catalog
	streams           *apiproxy.EventStreams
	frames            *apiproxy.LiveFrameSource
	workspaces        *apiproxy.WorkspaceGateway
	interactions      *apiproxy.InteractionGateway
	questions         *userquestions.ProviderHandle
	stopLifetimeClose func() bool
}

// New constructs an inactive API Proxy Plugin.
func New(deploymentSettings Settings, options RuntimeOptions) (*Plugin, error) {
	if strings.TrimSpace(deploymentSettings.Version) == "" ||
		deploymentSettings.Version != strings.TrimSpace(deploymentSettings.Version) {
		return nil, errors.New("apiproxy: version must be non-empty and trimmed")
	}
	if strings.TrimSpace(options.WorkingDirectory) == "" ||
		options.WorkingDirectory != strings.TrimSpace(options.WorkingDirectory) {
		return nil, errors.New(
			"apiproxy: working directory must be non-empty and trimmed",
		)
	}
	if options.EnsureDirectory == nil {
		return nil, errors.New("apiproxy: directory provisioner is required")
	}
	return &Plugin{
		deploymentSettings: deploymentSettings,
		options:            options,
	}, nil
}

// Manifest declares the complete API Proxy dependency, Service, Event, and
// approval Waterfall contract.
func (owner *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: apiproxy.PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[apiproxy.Service](owner),
		},
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[agent.Registry](),
			plugin.ServiceOf[agent.Constructor](),
			plugin.ServiceOf[agent.ScopeProvisioning](),
			plugin.ServiceOf[agentdefaultmodel.DefaultModel](),
			plugin.ServiceOf[commands.Registry](),
			plugin.ServiceOf[llm.LlmRuntime](),
			plugin.ServiceOf[session.LiveStore](),
			plugin.ServiceOf[sesspersist.Persistence](),
			plugin.ServiceOf[sessionprojection.Registry](),
			plugin.ServiceOf[sessionquery.QueryService](),
			plugin.ServiceOf[sessiontitle.TitleService](),
			plugin.ServiceOf[userquestions.UserQuestions](),
			plugin.ServiceOf[workspace.Registry](),
			plugin.ServiceOf[credentials.Provider](),
		},
		Events: []plugin.EventSubscription{
			plugin.EventOf[session.EventAppended](),
			plugin.EventOf[session.Created](),
			plugin.EventOf[session.Disposed](),
			plugin.EventOf[agent.StatusChanged](),
			plugin.EventOf[agent.AgentError](),
			plugin.EventOf[sessionprojection.Changed](),
			plugin.EventOf[workspace.ChangedNotice](),
			plugin.EventOf[workspace.RemovedNotice](),
			plugin.EventOf[workspace.OrderChangedNotice](),
			plugin.EventOf[workspace.ArchivedSessionsChangedNotice](),
		},
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf(owner),
		},
	}
}

// Apply resolves domain capabilities and constructs the complete Host adapter
// before its Service, observers, and Middleware become visible.
func (owner *Plugin) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	agents, err := plugin.Require[agent.Registry](owner)
	if err != nil {
		return err
	}
	constructor, err := plugin.Require[agent.Constructor](owner)
	if err != nil {
		return err
	}
	scopeProvisioning, err := plugin.Require[agent.ScopeProvisioning](owner)
	if err != nil {
		return err
	}
	defaults, err := plugin.Require[agentdefaultmodel.DefaultModel](owner)
	if err != nil {
		return err
	}
	commandRegistry, err := plugin.Require[commands.Registry](owner)
	if err != nil {
		return err
	}
	models, err := plugin.Require[llm.LlmRuntime](owner)
	if err != nil {
		return err
	}
	sessions, err := plugin.Require[session.LiveStore](owner)
	if err != nil {
		return err
	}
	persistence, err := plugin.Require[sesspersist.Persistence](owner)
	if err != nil {
		return err
	}
	projections, err := plugin.Require[sessionprojection.Registry](owner)
	if err != nil {
		return err
	}
	queries, err := plugin.Require[sessionquery.QueryService](owner)
	if err != nil {
		return err
	}
	titles, err := plugin.Require[sessiontitle.TitleService](owner)
	if err != nil {
		return err
	}
	questions, err := plugin.Require[userquestions.UserQuestions](owner)
	if err != nil {
		return err
	}
	workspaces, err := plugin.Require[workspace.Registry](owner)
	if err != nil {
		return err
	}
	credentialProvider, err := plugin.Require[credentials.Provider](owner)
	if err != nil {
		return err
	}

	sessionGateway, err := sessionapi.NewGateway(
		requestContext,
		sessionapi.Dependencies{
			Agents:      agents,
			Constructor: constructor,
			Scopes:      scopeProvisioning,
			Sessions:    sessions,
			Persistence: persistence,
			LLM:         models,
			Defaults:    defaults,
			Projections: projections,
			Titles:      titles,
			Workspaces:  workspaces,
			Directories: sessionapi.DirectoryProvisionerFunc(
				owner.options.EnsureDirectory,
			),
		},
		sessionapi.Options{
			WorkingDirectory: owner.options.WorkingDirectory,
			NewSessionID:     owner.options.NewSessionID,
		},
	)
	if err != nil {
		return err
	}
	frames, err := apiproxy.NewLiveFrameSource(
		apiproxy.LiveFrameDependencies{
			Sessions:    sessions,
			Projections: projections,
		},
		apiproxy.LiveFrameOptions{
			NewRPCID: owner.options.NewRPCID,
		},
	)
	if err != nil {
		return err
	}
	owner.frames = frames

	methods := apiproxy.NewCatalog()
	if err = registerMethods(
		methods,
		sessionGateway,
		queries,
		commandRegistry,
		models,
		credentialProvider,
		agents,
		defaults,
		owner.deploymentSettings,
		owner.options,
	); err != nil {
		return err
	}
	workspaceGateway, err := apiproxy.NewWorkspaceGateway(workspaces, frames)
	if err != nil {
		return err
	}
	if err = apiproxy.RegisterWorkspaceAPI(methods, workspaceGateway); err != nil {
		return err
	}
	interactions, err := apiproxy.NewInteractionGateway(
		apiproxy.InteractionGatewayDependencies{
			Methods: methods,
			Frames:  frames.InteractionBroker(),
		},
		apiproxy.InteractionGatewayOptions{
			NewRPCID:      owner.options.NewRPCID,
			ObserverError: owner.options.ObserverError,
		},
	)
	if err != nil {
		return err
	}
	providerHandle, err := questions.RegisterProvider(interactions)
	if err != nil {
		return err
	}
	streams, err := apiproxy.NewEventStreams(frames.Mux, frames.Host)
	if err != nil {
		providerHandle.Unregister()
		return err
	}
	owner.methods = methods
	owner.streams = streams
	owner.workspaces = workspaceGateway
	owner.interactions = interactions
	owner.questions = providerHandle
	owner.stopLifetimeClose = context.AfterFunc(
		plugin.Lifetime(owner),
		func() {
			_ = interactions.Close(context.Background())
		},
	)
	return nil
}

// Dispose withdraws interactive registration, settles pending interactions,
// and closes both event streams.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	if owner.stopLifetimeClose != nil {
		owner.stopLifetimeClose()
	}
	if owner.questions != nil {
		owner.questions.Unregister()
	}
	var closeErr error
	if owner.interactions != nil {
		closeErr = owner.interactions.Close(closeContext)
	}
	if owner.frames != nil {
		owner.frames.Close()
	}
	owner.methods = nil
	owner.streams = nil
	owner.frames = nil
	owner.workspaces = nil
	owner.interactions = nil
	owner.questions = nil
	owner.stopLifetimeClose = nil
	return closeErr
}

// ObserveEvent routes only the Event set declared in Manifest.
func (owner *Plugin) ObserveEvent(
	requestContext context.Context,
	fact plugin.Event,
) error {
	var frameErr error
	if owner.frames != nil {
		frameErr = owner.frames.ObserveEvent(requestContext, fact)
	}
	var workspaceErr error
	if owner.workspaces != nil {
		workspaceErr = owner.workspaces.ObserveEvent(requestContext, fact)
	}
	return errors.Join(frameErr, workspaceErr)
}

// Intercept delegates interactive approval to the correlation owner.
func (owner *Plugin) Intercept(
	requestContext context.Context,
	input approval.DecisionRequest,
	downstream plugin.WaterfallAction[approval.DecisionRequest, approval.Decision],
) (approval.Decision, error) {
	if owner.interactions == nil {
		return downstream.Execute(requestContext, input)
	}
	return owner.interactions.ResolveApproval(requestContext, input, downstream)
}

// HasUnary reports whether one canonical method is registered.
func (owner *Plugin) HasUnary(method string) bool {
	return owner.methods != nil && owner.methods.HasUnary(method)
}

// DispatchUnary decodes and invokes one canonical unary method.
func (owner *Plugin) DispatchUnary(
	requestContext context.Context,
	method string,
	rpcID connection.RPCID,
	payload json.RawMessage,
) (connection.RPCResult, error) {
	if owner.methods == nil {
		return connection.RPCResult{}, errors.New("apiproxy: service is not active")
	}
	return owner.methods.DispatchUnary(requestContext, method, rpcID, payload)
}

// Respond routes one client response to the pending interaction table.
func (owner *Plugin) Respond(
	requestContext context.Context,
	response connection.ClientResponse,
) (connection.RPCReceipt, error) {
	if owner.methods == nil {
		return connection.RPCReceipt{}, errors.New("apiproxy: service is not active")
	}
	return owner.methods.Respond(requestContext, response)
}

// Mux opens one independent Mux event stream.
func (owner *Plugin) Mux(
	requestContext context.Context,
	emit func(connection.RPCRequest) error,
) error {
	if owner.streams == nil {
		return errors.New("apiproxy: service is not active")
	}
	return owner.streams.Mux(requestContext, emit)
}

// Host opens one independent Host event stream.
func (owner *Plugin) Host(
	requestContext context.Context,
	emit func(connection.RPCRequest) error,
) error {
	if owner.streams == nil {
		return errors.New("apiproxy: service is not active")
	}
	return owner.streams.Host(requestContext, emit)
}

var _ apiproxy.Service = (*Plugin)(nil)
var _ plugin.EventObserver = (*Plugin)(nil)
var _ plugin.WaterfallMiddleware[
	approval.DecisionRequest,
	approval.Decision,
] = (*Plugin)(nil)
