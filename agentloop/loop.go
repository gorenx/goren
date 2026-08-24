package agentloop

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// loop is the one execution loop owned by one ReactLoopAgent. It composes
// activity admission with the durable Turn state machine and is deliberately
// not a Runtime Service or a second Agent identity.
type loop struct {
	activity *activityCoordinator
	turns    *turnRunner
}

func newLoop(
	subject *ReactLoopAgent,
	pending *agent.Inbox,
	projection *runtimeContextProjection,
	events *agentEventPublisher,
	lastTurn int64,
) (*loop, error) {
	if subject == nil || pending == nil || projection == nil || events == nil {
		return nil, errors.New("agentloop: Agent loop dependencies are incomplete")
	}
	activity := newActivityCoordinator(
		subject,
		pending,
		events,
		lastTurn,
	)
	turns := newTurnRunner(
		subject,
		activity,
		pending,
		projection,
		events,
	)
	activity.turns = turns
	return &loop{
		activity: activity,
		turns:    turns,
	}, nil
}

func (machine *loop) activate(
	requestContext context.Context,
	sessions session.LiveStore,
	models llm.LlmRuntime,
	toolRuntime tools.ToolRuntime,
	prompts systemprompt.Assembler,
	maxParallelToolCalls int,
) error {
	if machine == nil {
		return errors.New("agentloop: Agent loop is nil")
	}
	return machine.turns.activate(
		requestContext,
		sessions,
		models,
		toolRuntime,
		prompts,
		maxParallelToolCalls,
	)
}

func (machine *loop) beginServing() error {
	return machine.activity.beginServing()
}

func (machine *loop) dispose(closeContext context.Context) error {
	if machine == nil {
		return nil
	}
	idleErr := machine.quiesce(closeContext)
	machine.turns.deactivate()
	return idleErr
}

func (machine *loop) quiesce(closeContext context.Context) error {
	machine.activity.beginDispose()
	return machine.activity.whenIdle(closeContext)
}

func (machine *loop) status() agent.Status {
	return machine.activity.status()
}

func (machine *loop) send(
	input llm.UserMessage,
	target agent.InboxTarget,
	wakeup bool,
) error {
	return machine.activity.send(input, target, wakeup)
}

func (machine *loop) cancel(
	cause agent.CancelCause,
	options agent.CancelOptions,
) {
	machine.activity.cancel(cause, options)
}

func (machine *loop) whenIdle(requestContext context.Context) error {
	return machine.activity.whenIdle(requestContext)
}

func (machine *loop) runMaintenance(
	requestContext context.Context,
	operation func(context.Context) error,
) error {
	return machine.activity.runMaintenance(requestContext, operation)
}

func (machine *loop) runtimeContextView() *runtimeContextProjection {
	return machine.turns.projection
}
