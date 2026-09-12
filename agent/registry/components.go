package registry

import (
	"errors"
	"time"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	commandactor "github.com/gorenx/goren/commands/actor"
	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
	sessionactor "github.com/gorenx/goren/session/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// installation binds owner-specific clients. Readiness and resource ownership
// live in the business Registry; constructing clients starts no actor.
type installation struct {
	// components maps each component kind (and additional-component index) to
	// the client that initializes that responsibility for this Agent creation.
	components   map[agent.Component]initializer
	executor     *agentClient
	tools        *toolsClient
	commands     *commandsClient
	capabilities []*capabilityClient
}

func newInstallation(
	authority *goakt.GrainIdentity,
	input CreateAgent,
	releases map[agent.Resource]releaser,
) *installation {
	conversation := &sessionClient{
		owner:  input.SessionRegistry,
		role:   input.Role,
		parent: input.Parent,
		input: sessionregistry.RegisterSession{
			SessionID:  input.Creation.AgentID,
			CreationID: input.Creation.CreationID,
			Restore:    input.Restore,
			Options:    input.Options,
			Storage:    input.Storage,
		},
	}
	tools := &toolsClient{
		command: runtimeactor.InitializeRuntime{
			Configuration: input.configuration,
			AgentID:       input.Creation.AgentID,
			CreationID:    input.Creation.CreationID,
			Authority:     authority,
			Registry:      input.ToolsRegistry,
			Config:        input.Tools,
			Timeout:       input.Timeout,
		}}
	prompt := &promptClient{
		tools: tools,
		input: promptactor.InitializeRuntime{
			Config:         input.Prompt,
			AgentID:        input.Creation.AgentID,
			PromptRegistry: input.PromptRegistry,
			CreationID:     input.Creation.CreationID,
			Sections:       input.PromptSections,
		}}
	executor := &agentClient{
		modelRouter:  input.ModelRouter,
		middleware:   input.Middleware,
		handlers:     input.Handlers.Clone(),
		tools:        tools,
		timeout:      input.Timeout,
		authority:    authority,
		creation:     input.Creation,
		conversation: conversation,
		prompt:       prompt,
	}
	models := &modelClient{
		owner:        input.ModelConfig,
		creation:     input.Creation,
		conversation: conversation,
		options:      input.AgentOptions,
	}
	installed := &installation{
		components: map[agent.Component]initializer{
			{Kind: agent.SessionComponent}: conversation,
			{Kind: agent.PromptComponent}:  prompt,
			{Kind: agent.ToolsComponent}:   tools,
			{Kind: agent.ModelComponent}:   models,
			{Kind: agent.AgentComponent}:   executor,
		},
		executor: executor,
		tools:    tools,
	}
	releases[agent.Resource{Kind: agent.Conversation}] = conversation
	releases[agent.Resource{Kind: agent.ToolsRuntime}] = tools
	releases[agent.Resource{Kind: agent.PromptRuntime}] = prompt
	installExecution(releases, executor)
	if input.Commands != nil {
		installed.commands = &commandsClient{
			owner:        input.Commands,
			creation:     input.Creation,
			conversation: conversation,
			timeout:      input.Timeout,
		}
		installed.components[agent.Component{Kind: agent.CommandsComponent}] = installed.commands
		releases[agent.Resource{Kind: agent.CommandRuntime}] = installed.commands
	}
	for index, route := range input.Components {
		endpoint := &capabilityClient{
			role:         input.Role,
			owner:        route,
			creation:     input.Creation,
			conversation: conversation,
			executor:     executor,
		}
		installed.capabilities = append(installed.capabilities, endpoint)
		installed.components[agent.Component{
			Kind:  agent.AdditionalComponent,
			Index: index,
		}] = endpoint
		releases[agent.Resource{
			Kind:  agent.Capability,
			Index: index,
		}] = endpoint
	}
	return installed
}

func installExecution(releases map[agent.Resource]releaser, endpoint *agentClient) {
	releases[agent.Resource{Kind: agent.Work}] = &executionWait{endpoint: endpoint}
	releases[agent.Resource{Kind: agent.Execution}] = &executionRelease{endpoint: endpoint}
	releases[agent.Resource{Kind: agent.Activation}] = &activationRelease{endpoint: endpoint}
}

func (installed *installation) commandRoute() *goakt.GrainIdentity {
	if installed.commands == nil {
		return nil
	}
	return installed.commands.Runtime()
}

func (installed *installation) capabilityRoutes() []Component {
	result := make([]Component, 0, len(installed.capabilities))
	for _, endpoint := range installed.capabilities {
		result = append(result,
			Component{
				Owner:   endpoint.owner,
				Runtime: endpoint.Runtime(),
			},
		)
	}
	return result
}

type sessionClient struct {
	role     agent.Role
	parent   *Creation
	owner    *goakt.GrainIdentity
	input    sessionregistry.RegisterSession
	identity *goakt.GrainIdentity
}

func (endpoint *sessionClient) Runtime() *goakt.GrainIdentity { return endpoint.identity }

func (endpoint *sessionClient) Initialize(gc *goakt.GrainContext, reply chan<- componentResult) {
	if endpoint.parent != nil && !endpoint.input.Restore {
		identifier := endpoint.parent.AgentID
		endpoint.input.Options.Metadata.ParentSession = &identifier
	}
	requestComponent(gc, endpoint.owner, endpoint.input,
		func(gc *goakt.GrainContext, registered sessionregistry.Completion, failure error) {
			if failure != nil {
				reply <- componentResult{err: failure}
				return
			}
			if registered.Err != nil {
				reply <- componentResult{
					err:     registered.Err,
					unowned: true,
				}
				return
			}
			requestComponent(
				gc, endpoint.owner,
				sessionregistry.StartSession{
					SessionID:  endpoint.input.SessionID,
					CreationID: endpoint.input.CreationID,
				},
				func(gc *goakt.GrainContext, initialized sessionactor.Initialized, failure error) {
					if failure == nil {
						failure = initialized.Err
					}
					if failure == nil {
						endpoint.identity, failure = goakt.GrainOf[*sessionactor.Grain](
							gc.Context(),
							gc.ActorSystem(),
							sessionactor.RuntimeName(string(endpoint.input.SessionID)),
							goakt.WithLongLivedGrain(),
						)
					}
					if failure != nil {
						reply <- componentResult{err: failure}
						return
					}
					if (initialized.Header.ParentSession == nil) != (endpoint.parent == nil) || endpoint.parent != nil && *initialized.Header.ParentSession != endpoint.parent.AgentID {
						reply <- componentResult{err: errors.New("agent registry: restored Session parent differs")}
						return
					}
					endpoint.admitRole(gc, reply)
				},
			)
		},
	)
}

func (endpoint *sessionClient) Close(gc *goakt.GrainContext, reply chan<- error) {
	requestComponent(gc, endpoint.owner, sessionregistry.CloseSession{
		SessionID:  endpoint.input.SessionID,
		CreationID: endpoint.input.CreationID,
	}, func(gc *goakt.GrainContext, closed sessionactor.Closed, failure error) {
		reply <- errors.Join(failure, closed.Err)
	})
}

type promptClient struct {
	tools   *toolsClient
	input   promptactor.InitializeRuntime
	release runtimeRelease
}

func (endpoint *promptClient) Runtime() *goakt.GrainIdentity { return endpoint.release.identity }

func (endpoint *promptClient) Initialize(gc *goakt.GrainContext, reply chan<- componentResult) {
	endpoint.input.ToolsRuntime = endpoint.tools.Runtime()
	identity, err := goakt.GrainOf[*promptactor.RuntimeGrain](
		gc.Context(),
		gc.ActorSystem(),
		promptactor.RuntimeName(string(endpoint.input.AgentID)),
		goakt.WithLongLivedGrain(),
	)
	endpoint.release.identity = identity
	if err != nil {
		reply <- componentResult{err: err}
		return
	}
	requestComponent(
		gc,
		identity,
		endpoint.input,
		func(gc *goakt.GrainContext, initialized promptactor.RuntimeInitialized, failure error) {
			reply <- componentResult{err: errors.Join(failure, initialized.Err)}
		},
	)
}

func (endpoint *promptClient) Close(gc *goakt.GrainContext, reply chan<- error) {
	closeRuntime(
		gc,
		&endpoint.release,
		promptactor.CloseRuntime{},
		func(closed promptactor.Completion) error { return closed.Err },
		reply,
	)
}

type modelClient struct {
	owner        *goakt.GrainIdentity
	conversation *sessionClient
	creation     Creation
	options      *agent.Options
}

func (endpoint *modelClient) Runtime() *goakt.GrainIdentity { return endpoint.owner }

func (endpoint *modelClient) Initialize(gc *goakt.GrainContext, reply chan<- componentResult) {
	if endpoint.options == nil {
		requestComponent(gc, endpoint.owner, llmactor.InitializeAgent{
			AgentID:    string(endpoint.creation.AgentID),
			CreationID: endpoint.creation.CreationID,
			Session:    endpoint.conversation.Runtime(),
		}, func(_ *goakt.GrainContext, initialized llmactor.Completion, failure error) {
			reply <- componentResult{err: errors.Join(failure, initialized.Err)}
		})
		return
	}
	// Explicit creation settings replace the previous lifetime's selection.
	// Resolve omitted fields against deployment defaults, never Session history.
	requestComponent(gc, endpoint.owner, llmactor.ResolveAgent{
		Overrides: llm.CallConfig{
			Provider:  endpoint.options.Provider,
			Model:     endpoint.options.Model,
			MaxTokens: endpoint.options.Clone().MaxTokens,
		},
	}, func(gc *goakt.GrainContext, resolved llmactor.AgentConfiguration, failure error) {
		if failure = errors.Join(failure, resolved.Err); failure != nil {
			reply <- componentResult{err: failure}
			return
		}
		requestComponent(gc, endpoint.owner, llmactor.SetConfiguration{
			AgentID: string(endpoint.creation.AgentID),
			Config:  resolved.Resolved.Config,
		}, func(_ *goakt.GrainContext, configured llmactor.ConfigurationResult, failure error) {
			reply <- componentResult{err: errors.Join(failure, configured.Err)}
		})
	})
}

type agentClient struct {
	modelRouter  *goakt.PID
	middleware   *goakt.GrainIdentity
	handlers     middlewareactor.Handlers
	tools        *toolsClient
	timeout      time.Duration
	authority    *goakt.GrainIdentity
	creation     Creation
	conversation *sessionClient
	prompt       *promptClient
	identity     *goakt.GrainIdentity
}

func (endpoint *agentClient) Runtime() *goakt.GrainIdentity { return endpoint.identity }

func (endpoint *agentClient) Initialize(gc *goakt.GrainContext, reply chan<- componentResult) {
	identity, err := goakt.GrainOf[*loopactor.Grain](
		gc.Context(), gc.ActorSystem(),
		loopactor.RuntimeName(string(endpoint.creation.AgentID)),
		goakt.WithLongLivedGrain(),
	)
	endpoint.identity = identity
	if err != nil {
		reply <- componentResult{err: err}
		return
	}
	requestComponent(
		gc,
		identity,
		agentactor.Initialize{
			AgentID:     endpoint.creation.AgentID,
			CreationID:  endpoint.creation.CreationID,
			Authority:   endpoint.authority,
			Session:     endpoint.conversation.Runtime(),
			Prompt:      endpoint.prompt.Runtime(),
			ModelRouter: endpoint.modelRouter,
			Middleware:  endpoint.middleware,
			Handlers:    endpoint.handlers.Clone(),
			Tools:       endpoint.tools.Runtime(),
			Timeout:     endpoint.timeout,
		},
		func(gc *goakt.GrainContext, initialized agentactor.Initialized, failure error) {
			reply <- componentResult{err: errors.Join(failure, initialized.Err)}
		},
	)
}

// executionWait confirms the active execution has settled before dependencies close.
type executionWait struct {
	endpoint *agentClient
}

func (request *executionWait) Close(gc *goakt.GrainContext, reply chan<- error) {
	requestComponent(
		gc,
		request.endpoint.Runtime(),
		agentactor.PrepareClose{CreationID: request.endpoint.creation.CreationID},
		func(gc *goakt.GrainContext, closed agentactor.Prepared, failure error) {
			reply <- errors.Join(failure, closed.Err)
		},
	)
}

// executionRelease closes execution resources while its endpoint remains alive.
type executionRelease struct {
	endpoint *agentClient
}

func (request *executionRelease) Close(gc *goakt.GrainContext, reply chan<- error) {
	requestComponent(
		gc,
		request.endpoint.Runtime(),
		agentactor.CloseResources{CreationID: request.endpoint.creation.CreationID},
		func(gc *goakt.GrainContext, closed agentactor.Closed, failure error) {
			reply <- errors.Join(failure, closed.Err)
		},
	)
}

// activationRelease stops the endpoint after Session has confirmed flush and close.
type activationRelease struct {
	endpoint *agentClient
}

func (request *activationRelease) Close(gc *goakt.GrainContext, reply chan<- error) {
	deactivate(gc, request.endpoint.Runtime(), reply)
}

type commandsClient struct {
	owner        *goakt.GrainIdentity
	creation     Creation
	conversation *sessionClient
	timeout      time.Duration
	release      runtimeRelease
}

func (endpoint *commandsClient) Runtime() *goakt.GrainIdentity { return endpoint.release.identity }
func (endpoint *commandsClient) Initialize(gc *goakt.GrainContext, reply chan<- componentResult) {
	identity, err := goakt.GrainOf[*commandactor.Grain](
		gc.Context(), gc.ActorSystem(),
		commandactor.RuntimeName(string(endpoint.creation.AgentID)),
		goakt.WithLongLivedGrain(),
	)
	endpoint.release.identity = identity
	if err != nil {
		reply <- componentResult{err: err}
		return
	}
	requestComponent(
		gc,
		identity, commandactor.Initialize{
			SessionID:  endpoint.creation.AgentID,
			CreationID: endpoint.creation.CreationID,
			Session:    endpoint.conversation.Runtime(),
			Registry:   endpoint.owner,
			Timeout:    endpoint.timeout,
		},
		func(gc *goakt.GrainContext, initialized commandactor.Completion, failure error) {
			reply <- componentResult{err: errors.Join(failure, initialized.Err)}
		},
	)
}

func (endpoint *commandsClient) Close(gc *goakt.GrainContext, reply chan<- error) {
	closeRuntime(
		gc,
		&endpoint.release,
		commandactor.Close{CreationID: endpoint.creation.CreationID},
		func(closed commandactor.Completion) error { return closed.Err },
		reply,
	)
}

type capabilityClient struct {
	role         agent.Role
	owner        *goakt.GrainIdentity
	creation     Creation
	conversation *sessionClient
	executor     *agentClient
	identity     *goakt.GrainIdentity
}

func (endpoint *capabilityClient) Runtime() *goakt.GrainIdentity { return endpoint.identity }
func (endpoint *capabilityClient) Initialize(gc *goakt.GrainContext, reply chan<- componentResult) {
	requestComponent(
		gc, endpoint.owner,
		agentactor.StartComponent{
			Role:       endpoint.role,
			AgentID:    endpoint.creation.AgentID,
			CreationID: endpoint.creation.CreationID,
			Agent:      endpoint.executor.Runtime(),
			Session:    endpoint.conversation.Runtime(),
		},
		func(gc *goakt.GrainContext, initialized agentactor.ComponentStarted, failure error) {
			endpoint.identity = initialized.Runtime
			if failure == nil {
				failure = initialized.Err
			}
			if failure == nil && endpoint.identity == nil {
				failure = errors.New("agent registry: missing component runtime")
			}
			reply <- componentResult{err: failure}
		},
	)
}

func (endpoint *capabilityClient) Close(gc *goakt.GrainContext, reply chan<- error) {
	requestComponent(
		gc,
		endpoint.owner,
		agentactor.StopComponent{
			Role:       endpoint.role,
			AgentID:    endpoint.creation.AgentID,
			CreationID: endpoint.creation.CreationID,
		},
		func(gc *goakt.GrainContext, closed agentactor.ComponentStopped, failure error) {
			reply <- errors.Join(failure, closed.Err)
		},
	)
}

type childClient struct {
	creation Creation
}

func (endpoint *childClient) Close(gc *goakt.GrainContext, reply chan<- error) {
	requestComponent(
		gc, gc.Self(),
		CloseAgent{Creation: endpoint.creation},
		func(gc *goakt.GrainContext, closed agentactor.Closed, failure error) {
			reply <- errors.Join(failure, closed.Err)
		},
	)
}

func (endpoint *sessionClient) admitRole(gc *goakt.GrainContext, reply chan<- componentResult) {
	requestComponent(gc, endpoint.identity, sessionactor.Commit{
		CreationID: endpoint.input.CreationID,
		Plan: agent.RoleAdmission{
			SessionID: endpoint.input.SessionID,
			Expected:  endpoint.role,
			Restore:   endpoint.input.Restore,
		},
	}, func(gc *goakt.GrainContext, committed sessionactor.Committed, failure error) {
		if failure == nil {
			failure = committed.Err
		}
		if failure != nil {
			reply <- componentResult{err: failure}
			return
		}
		requestComponent(gc, endpoint.identity, sessionactor.Flush{
			CreationID: endpoint.input.CreationID,
			Barrier:    committed.Result.Barrier,
		}, func(_ *goakt.GrainContext, flushed sessionactor.Flushed, failure error) {
			reply <- componentResult{err: errors.Join(failure, flushed.Err)}
		})
	})
}
