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

// ReactLoopAgent is the concrete Agent capability and the root Plugin of one
// scoped Agent tree. agentDriver exclusively owns live execution state.
type ReactLoopAgent struct {
	plugin.Base
	owner        *LoopPlugin
	identifier   session.SessionID
	options      agent.Options
	conversation *session.Session
	pending      *agent.Inbox
	driver       *agentDriver
	lifecycle    *agentLifecycle
	children     []plugin.ChildPlugin
}

func newReactLoopAgent(
	owner *LoopPlugin,
	conversation *session.Session,
	loopOptions agent.Options,
) (*ReactLoopAgent, error) {
	if owner == nil || conversation == nil {
		return nil, errors.New("agentloop: Agent owner and Session are required")
	}
	lastTurn, err := restoreLastTurn(conversation)
	if err != nil {
		return nil, err
	}
	subject := &ReactLoopAgent{
		owner:        owner,
		identifier:   conversation.ID(),
		options:      cloneAgentOptions(loopOptions),
		conversation: conversation,
	}
	pending, err := agent.NewInbox(
		conversation,
		inboxEventBridge{
			subject: subject,
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
	subject.driver = newAgentDriver(subject, pending, projection, lastTurn)
	return subject, nil
}

// Manifest provides the exact scoped Agent Service and declares every runtime
// capability used by its driver and child Plugins.
func (subject *ReactLoopAgent) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName + "/react-loop-agent",
		Provides: []plugin.ServiceType{
			plugin.ServiceOf[agent.Agent](),
		},
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[session.LiveStore](),
			plugin.ServiceOf[llm.LlmRuntime](),
			plugin.ServiceOf[tools.ToolRuntime](),
			plugin.ServiceOf[systemprompt.Assembler](),
		},
		Children: append([]plugin.ChildPlugin(nil), subject.children...),
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
	return subject.driver.activate(
		requestContext,
		sessions,
		models,
		toolRuntime,
		prompts,
	)
}

// Dispose stops live execution. agentMembership separately owns externally
// visible Agent and Session membership.
func (subject *ReactLoopAgent) Dispose(closeContext context.Context) error {
	disposeErr := subject.driver.dispose(closeContext)
	if subject.lifecycle != nil {
		subject.lifecycle.markTreeStopped()
	}
	return disposeErr
}

func (subject *ReactLoopAgent) ID() session.SessionID { return subject.identifier }

func (subject *ReactLoopAgent) OptionsValue() agent.Options {
	return cloneAgentOptions(subject.options)
}

func (subject *ReactLoopAgent) SessionValue() *session.Session {
	return subject.conversation
}

func (subject *ReactLoopAgent) InboxValue() *agent.Inbox { return subject.pending }

func (subject *ReactLoopAgent) StatusValue() agent.Status {
	return subject.driver.status()
}

func (subject *ReactLoopAgent) Send(
	input llm.UserMessage,
	target agent.InboxTarget,
	wakeup bool,
) error {
	return subject.driver.send(input, target, wakeup)
}

func (subject *ReactLoopAgent) Followup(input llm.UserMessage) error {
	return subject.driver.send(input, agent.NextTurn, true)
}

func (subject *ReactLoopAgent) Steer(input llm.UserMessage) error {
	return subject.driver.send(input, agent.NextStep, true)
}

func (subject *ReactLoopAgent) Inject(input llm.UserMessage) error {
	return subject.driver.send(input, agent.NextStep, false)
}

func (subject *ReactLoopAgent) Cancel(
	cause agent.CancelCause,
	options agent.CancelOptions,
) {
	subject.driver.cancel(cause, options)
}

func (subject *ReactLoopAgent) WhenIdle(requestContext context.Context) error {
	return subject.driver.whenIdle(requestContext)
}

func (subject *ReactLoopAgent) RunMaintenance(
	requestContext context.Context,
	task agent.MaintenanceTask,
) error {
	return subject.driver.runMaintenance(requestContext, task)
}

func cloneAgentOptions(source agent.Options) agent.Options {
	detached := source
	detached.MaxTokens = cloneInt(source.MaxTokens)
	return detached
}
