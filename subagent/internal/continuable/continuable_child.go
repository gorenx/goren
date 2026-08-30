package continuable

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	"github.com/gorenx/goren/subagent/internal/lineage"
)

var errChildRetired = errors.New(
	"subagent: Continuable child owner retired",
)

type startInput struct {
	parent            agent.Agent
	descriptor        subagent.ContinuableDescriptor
	request           subagent.ContinuableOptions
	lineage           lineage.Lineage
	seed              []session.Event
	seedBuilder       string
	seedLength        int64
	identityRequested bool
}

type startRequest struct {
	ctx   context.Context
	input startInput
	reply chan startResult
}

type startResult struct {
	execution subagent.Execution
	err       error
}

type resumeRequest struct {
	ctx     context.Context
	parent  agent.Agent
	message agentmessage.UserMessage
	reply   chan resumeResult
}

type resumeResult struct {
	identifier agentmessage.MessageID
	err        error
}

type interruptRequest struct {
	reply chan struct{}
}

type shutdownRequest struct {
	ctx   context.Context
	reply chan error
}

type executionClosed struct {
	executionRunID subagent.RunID
	parentID       session.SessionID
}

// continuableChild owns command order and the current resident execution for
// one durable Continuable child. Its mailbox serializes only this child, so
// different child identities continue independently.
type continuableChild struct {
	id           session.SessionID
	agents       agent.Registry
	materializer *materializer
	registry     *continuableChildRegistry
	mailbox      chan any
	settlement   chan struct{}
	closed       chan executionClosed
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	current      *residentExecution
	retirable    bool
}

func newContinuableChild(
	dependencySet Dependencies,
	factory *materializer,
	registry *continuableChildRegistry,
	childSessionID session.SessionID,
) *continuableChild {
	childContext, cancelChild := context.WithCancel(context.Background())
	return &continuableChild{
		id:           childSessionID,
		agents:       dependencySet.Agents,
		materializer: factory,
		registry:     registry,
		mailbox:      make(chan any),
		settlement:   make(chan struct{}, 1),
		closed:       make(chan executionClosed, 1),
		ctx:          childContext,
		cancel:       cancelChild,
		done:         make(chan struct{}),
	}
}

func (child *continuableChild) run() {
	defer close(child.done)
	defer child.cancel()
	for {
		if child.current == nil && child.retirable {
			select {
			case received := <-child.mailbox:
				if child.handleCommand(received) {
					return
				}
			case closed := <-child.closed:
				child.handleExecutionClosed(closed)
			case <-child.settlement:
			default:
				child.registry.retire(child)
				return
			}
			continue
		}
		select {
		case received := <-child.mailbox:
			if child.handleCommand(received) {
				return
			}
		case closed := <-child.closed:
			child.handleExecutionClosed(closed)
		case <-child.settlement:
			if child.current != nil {
				child.current.notify()
			}
		}
	}
}

func (child *continuableChild) handleCommand(received any) bool {
	switch request := received.(type) {
	case startRequest:
		execution, err := child.handleStart(request.ctx, request.input)
		request.reply <- startResult{
			execution: execution,
			err:       err,
		}
	case resumeRequest:
		identifier, err := child.handleResume(
			request.ctx,
			request.parent,
			request.message,
		)
		request.reply <- resumeResult{
			identifier: identifier,
			err:        err,
		}
	case interruptRequest:
		child.handleInterrupt()
		request.reply <- struct{}{}
	case shutdownRequest:
		child.cancel()
		request.reply <- child.stopCurrentUntilClosing(request.ctx)
		return true
	}
	if child.current == nil {
		child.retirable = true
	}
	return false
}

func (child *continuableChild) handleExecutionClosed(
	closed executionClosed,
) {
	if child.current != nil &&
		child.current.RunID() == closed.executionRunID {
		child.current = nil
		child.retirable = true
	}
	child.registry.notify(closed.parentID)
}

func (child *continuableChild) start(
	ctx context.Context,
	input startInput,
) (subagent.Execution, error) {
	reply := make(chan startResult, 1)
	request := startRequest{
		ctx:   ctx,
		input: input,
		reply: reply,
	}
	select {
	case child.mailbox <- request:
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-child.done:
		return nil, errChildRetired
	}
	select {
	case response := <-reply:
		return response.execution, response.err
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

func (child *continuableChild) resume(
	ctx context.Context,
	parentAgent agent.Agent,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	reply := make(chan resumeResult, 1)
	request := resumeRequest{
		ctx:     ctx,
		parent:  parentAgent,
		message: messageValue,
		reply:   reply,
	}
	select {
	case child.mailbox <- request:
	case <-ctx.Done():
		return "", context.Cause(ctx)
	case <-child.done:
		return "", errChildRetired
	}
	select {
	case response := <-reply:
		return response.identifier, response.err
	case <-ctx.Done():
		return "", context.Cause(ctx)
	}
}

func (child *continuableChild) interrupt(ctx context.Context) error {
	reply := make(chan struct{}, 1)
	request := interruptRequest{
		reply: reply,
	}
	select {
	case child.mailbox <- request:
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-child.done:
		return errChildRetired
	}
	select {
	case <-reply:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (child *continuableChild) notify() {
	select {
	case child.settlement <- struct{}{}:
	case <-child.done:
	default:
	}
}

func (child *continuableChild) shutdown(ctx context.Context) error {
	child.cancel()
	reply := make(chan error, 1)
	request := shutdownRequest{
		ctx:   ctx,
		reply: reply,
	}
	select {
	case child.mailbox <- request:
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-child.done:
		return nil
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (child *continuableChild) stopCurrentUntilClosing(
	ctx context.Context,
) error {
	current := child.current
	if current == nil {
		return nil
	}
	current.Stop(sharedexecution.CloseModule)
	select {
	case <-current.handle.ClosingSignal():
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (child *continuableChild) operationContext(
	requestContext context.Context,
) (context.Context, context.CancelFunc) {
	workContext, cancelOperation := context.WithCancel(requestContext)
	cancelWatch := context.AfterFunc(child.ctx, cancelOperation)
	return workContext, func() {
		cancelWatch()
		cancelOperation()
	}
}
