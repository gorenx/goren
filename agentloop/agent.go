package agentloop

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// ReactLoopAgent is the concrete Agent business capability. Plugin binding and
// scoped dependency resolution belong to agentScopeRoot.
type ReactLoopAgent struct {
	identifier           session.SessionID
	options              agent.Options
	conversation         session.Context
	pending              *agent.Inbox
	loop                 *loop
	maxParallelToolCalls int
	scopeRuntime         agent.AgentScopeRuntime
}

func newReactLoopAgent(
	conversation session.Context,
	loopOptions agent.Options,
	maxParallelToolCalls int,
	failures observerFailureReporter,
	scopeRuntime agent.AgentScopeRuntime,
) (*ReactLoopAgent, error) {
	if conversation == nil {
		return nil, errors.New("agentloop: Agent Session is required")
	}
	if maxParallelToolCalls < 1 {
		return nil, errors.New(
			"agentloop: Agent Tool concurrency must be positive",
		)
	}
	if scopeRuntime == nil {
		return nil, errors.New("agentloop: Agent Scope Runtime is required")
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
		scopeRuntime:         scopeRuntime,
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

func (subject *ReactLoopAgent) activate(
	requestContext context.Context,
	sessions session.LiveStore,
	models llm.LlmRuntime,
	toolRuntime tools.ToolRuntime,
	prompts systemprompt.Assembler,
) error {
	if err := requestContext.Err(); err != nil {
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

func (subject *ReactLoopAgent) shutdown(closeContext context.Context) error {
	return subject.loop.dispose(closeContext)
}

func (subject *ReactLoopAgent) ID() session.SessionID { return subject.identifier }

func (subject *ReactLoopAgent) ScopeRuntimeValue() agent.AgentScopeRuntime {
	return subject.scopeRuntime
}

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
	input agentmessage.UserMessage,
	target agent.InboxTarget,
	wakeup bool,
) error {
	return subject.loop.send(input, target, wakeup)
}

func (subject *ReactLoopAgent) Followup(input agentmessage.UserMessage) error {
	return subject.loop.send(input, agent.NextTurn, true)
}

func (subject *ReactLoopAgent) Steer(input agentmessage.UserMessage) error {
	return subject.loop.send(input, agent.NextStep, true)
}

func (subject *ReactLoopAgent) Inject(input agentmessage.UserMessage) error {
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
	return detached
}
