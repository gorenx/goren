package agentloop

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// ReactLoopAgent is the concrete Agent capability Plugin inside one private
// scoped tree. It delegates live execution to named collaborators.
type ReactLoopAgent struct {
	plugin.Base
	identifier           session.SessionID
	options              agent.Options
	conversation         session.Context
	pending              *agent.Inbox
	loop                 *loop
	maxParallelToolCalls int
}

func newReactLoopAgent(
	conversation session.Context,
	loopOptions agent.Options,
	maxParallelToolCalls int,
	failures observerFailureReporter,
) (*ReactLoopAgent, error) {
	if conversation == nil {
		return nil, errors.New("agentloop: Agent Session is required")
	}
	if maxParallelToolCalls < 1 {
		return nil, errors.New(
			"agentloop: Agent Tool concurrency must be positive",
		)
	}
	lastTurn, err := restoreLastTurn(conversation)
	if err != nil {
		return nil, err
	}
	subject := &ReactLoopAgent{
		identifier:           conversation.ID(),
		options:              cloneAgentOptions(loopOptions),
		conversation:         conversation,
		maxParallelToolCalls: maxParallelToolCalls,
	}
	events := newAgentEventPublisher(subject, failures)
	pending, err := agent.NewInbox(
		conversation,
		inboxEventBridge{
			events: events,
		},
	)
	if err != nil {
		return nil, err
	}
	projection, err := newRuntimeContextProjection(conversation)
	if err != nil {
		return nil, err
	}
	subject.pending = pending
	machine, err := newLoop(
		subject,
		pending,
		projection,
		events,
		lastTurn,
	)
	if err != nil {
		return nil, err
	}
	subject.loop = machine
	return subject, nil
}

// Manifest provides the exact scoped Agent Service and declares every runtime
// capability used by its private loop.
func (subject *ReactLoopAgent) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName + "/react-loop-agent",
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[agent.Agent](subject),
		},
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[session.LiveStore](),
			plugin.ServiceOf[llm.LlmRuntime](),
			plugin.ServiceOf[tools.ToolRuntime](),
			plugin.ServiceOf[systemprompt.Assembler](),
		},
	}
}

// Apply resolves the exact Agent Scope overlays into its execution owner.
func (subject *ReactLoopAgent) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	sessions, err := plugin.Require[session.LiveStore](subject)
	if err != nil {
		return err
	}
	models, err := plugin.Require[llm.LlmRuntime](subject)
	if err != nil {
		return err
	}
	toolRuntime, err := plugin.Require[tools.ToolRuntime](subject)
	if err != nil {
		return err
	}
	prompts, err := plugin.Require[systemprompt.Assembler](subject)
	if err != nil {
		return err
	}
	return subject.loop.activate(
		requestContext,
		sessions,
		models,
		toolRuntime,
		prompts,
		subject.maxParallelToolCalls,
	)
}

// Dispose stops live execution. agentMembership separately owns externally
// visible Agent and Session membership.
func (subject *ReactLoopAgent) Dispose(closeContext context.Context) error {
	return subject.loop.dispose(closeContext)
}

func (subject *ReactLoopAgent) ID() session.SessionID { return subject.identifier }

func (subject *ReactLoopAgent) OptionsValue() agent.Options {
	return cloneAgentOptions(subject.options)
}

func (subject *ReactLoopAgent) SessionValue() session.Context {
	return subject.conversation
}

func (subject *ReactLoopAgent) InboxValue() *agent.Inbox { return subject.pending }

func (subject *ReactLoopAgent) StatusValue() agent.Status {
	return subject.loop.status()
}

func (subject *ReactLoopAgent) Send(
	input llm.UserMessage,
	target agent.InboxTarget,
	wakeup bool,
) error {
	return subject.loop.send(input, target, wakeup)
}

func (subject *ReactLoopAgent) Followup(input llm.UserMessage) error {
	return subject.loop.send(input, agent.NextTurn, true)
}

func (subject *ReactLoopAgent) Steer(input llm.UserMessage) error {
	return subject.loop.send(input, agent.NextStep, true)
}

func (subject *ReactLoopAgent) Inject(input llm.UserMessage) error {
	return subject.loop.send(input, agent.NextStep, false)
}

func (subject *ReactLoopAgent) Cancel(
	cause agent.CancelCause,
	options agent.CancelOptions,
) {
	subject.loop.cancel(cause, options)
}

func (subject *ReactLoopAgent) WhenIdle(requestContext context.Context) error {
	return subject.loop.whenIdle(requestContext)
}

func (subject *ReactLoopAgent) RunMaintenance(
	requestContext context.Context,
	operation func(context.Context) error,
) error {
	return subject.loop.runMaintenance(requestContext, operation)
}

func cloneAgentOptions(source agent.Options) agent.Options {
	detached := source
	detached.MaxTokens = cloneInt(source.MaxTokens)
	detached.SubagentDepth = cloneInt64(source.SubagentDepth)
	return detached
}
