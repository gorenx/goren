package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

type alignRequest struct {
	ctx   context.Context
	reply chan error
}

type messageRequest struct {
	ctx     context.Context
	message agentmessage.UserMessage
	reply   chan messageResult
}

type messageResult struct {
	identifier agentmessage.MessageID
	err        error
}

type interruptRequest struct {
	reply chan struct{}
}

type catchUpRequest struct {
	reply chan error
}

type shutdownRequest struct {
	ctx   context.Context
	reply chan error
}

type executionClosedNotice struct {
	epoch *residentEpoch
}

// boundChild is the sole serial worker for one immutable user Session
// Binding. It outlives individual resident Agent epochs.
type boundChild struct {
	key           bindingKey
	parent        agent.Agent
	agents        agent.Registry
	sessions      session.LiveStore
	projections   sessionprojection.Registry
	definitions   *definitionCatalog
	publisher     sharedexecution.EventPublisher
	executions    *sharedexecution.Registry
	failures      FailureReporter
	materializer  *materializer
	mailbox       chan any
	dirty         chan struct{}
	detach        chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	current       *residentEpoch
	floor         int64
	deliveryReady bool
}

func newBoundChild(
	registryContext context.Context,
	dependencySet Dependencies,
	factory *materializer,
	definitions *definitionCatalog,
	parentAgent agent.Agent,
	bindingValue subagentprojection.BoundBinding,
) *boundChild {
	childContext, cancelChild := context.WithCancel(registryContext)
	return &boundChild{
		key: bindingKey{
			parentID: parentAgent.ID(),
			name:     bindingValue.Name,
			childID:  bindingValue.ChildSessionID,
		},
		parent:       parentAgent,
		agents:       dependencySet.Agents,
		sessions:     dependencySet.Sessions,
		projections:  dependencySet.Projections,
		definitions:  definitions,
		publisher:    dependencySet.Publisher,
		executions:   dependencySet.Executions,
		failures:     dependencySet.Failures,
		materializer: factory,
		mailbox:      make(chan any, 16),
		dirty:        make(chan struct{}, 1),
		detach:       make(chan struct{}, 1),
		ctx:          childContext,
		cancel:       cancelChild,
		done:         make(chan struct{}),
		floor:        bindingValue.Seq + 1,
	}
}

func (child *boundChild) run() {
	defer close(child.done)
	defer child.cancel()
	for {
		select {
		case received := <-child.mailbox:
			if child.handleCommand(received) {
				return
			}
		case <-child.dirty:
			if err := child.catchUpInteractions(); err != nil {
				child.reportInteractionFailure(err)
			}
		case <-child.detach:
			return
		}
	}
}

func (child *boundChild) handleCommand(received any) bool {
	switch commandValue := received.(type) {
	case alignRequest:
		commandValue.reply <- child.handleAlignment(commandValue.ctx)
	case messageRequest:
		identifier, err := child.handleMessage(commandValue.ctx, commandValue.message)
		commandValue.reply <- messageResult{
			identifier: identifier,
			err:        err,
		}
	case interruptRequest:
		child.handleInterrupt()
		commandValue.reply <- struct{}{}
	case catchUpRequest:
		commandValue.reply <- child.catchUpInteractions()
	case shutdownRequest:
		child.cancel()
		commandValue.reply <- child.stopCurrent(commandValue.ctx)
		return true
	case executionClosedNotice:
		if child.current == commandValue.epoch {
			child.current = nil
		}
	}
	return false
}

func (child *boundChild) align(requestContext context.Context) error {
	reply := make(chan error, 1)
	commandValue := alignRequest{
		ctx:   requestContext,
		reply: reply,
	}
	select {
	case child.mailbox <- commandValue:
	case <-requestContext.Done():
		return context.Cause(requestContext)
	case <-child.done:
		return errors.New("subagent: Bound child is closed")
	}
	select {
	case err := <-reply:
		return err
	case <-requestContext.Done():
		return context.Cause(requestContext)
	}
}

func (child *boundChild) send(
	requestContext context.Context,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	reply := make(chan messageResult, 1)
	commandValue := messageRequest{
		ctx:     requestContext,
		message: messageValue,
		reply:   reply,
	}
	select {
	case child.mailbox <- commandValue:
	case <-requestContext.Done():
		return "", context.Cause(requestContext)
	case <-child.done:
		return "", errors.New("subagent: Bound child is closed")
	}
	select {
	case result := <-reply:
		return result.identifier, result.err
	case <-requestContext.Done():
		return "", context.Cause(requestContext)
	}
}

func (child *boundChild) interrupt(requestContext context.Context) error {
	reply := make(chan struct{}, 1)
	commandValue := interruptRequest{
		reply: reply,
	}
	select {
	case child.mailbox <- commandValue:
	case <-requestContext.Done():
		return context.Cause(requestContext)
	case <-child.done:
		return nil
	}
	select {
	case <-reply:
		return nil
	case <-requestContext.Done():
		return context.Cause(requestContext)
	}
}

func (child *boundChild) catchUp(requestContext context.Context) error {
	reply := make(chan error, 1)
	commandValue := catchUpRequest{
		reply: reply,
	}
	select {
	case child.mailbox <- commandValue:
	case <-requestContext.Done():
		return context.Cause(requestContext)
	case <-child.done:
		return errors.New("subagent: Bound child is closed")
	}
	select {
	case err := <-reply:
		return err
	case <-requestContext.Done():
		return context.Cause(requestContext)
	}
}

func (child *boundChild) notify() {
	select {
	case child.dirty <- struct{}{}:
	default:
	}
}

func (child *boundChild) executionClosed(closed *residentEpoch) {
	select {
	case child.mailbox <- executionClosedNotice{
		epoch: closed,
	}:
	case <-child.done:
	}
}

func (child *boundChild) shutdown(closeContext context.Context) error {
	child.cancel()
	reply := make(chan error, 1)
	commandValue := shutdownRequest{
		ctx:   closeContext,
		reply: reply,
	}
	select {
	case child.mailbox <- commandValue:
	case <-closeContext.Done():
		return context.Cause(closeContext)
	case <-child.done:
		return nil
	}
	select {
	case err := <-reply:
		return err
	case <-closeContext.Done():
		return context.Cause(closeContext)
	}
}

func (child *boundChild) dispose(requestContext context.Context) error {
	child.cancel()
	select {
	case child.detach <- struct{}{}:
	case <-child.done:
		return nil
	}
	select {
	case <-child.done:
		return nil
	case <-requestContext.Done():
		return context.Cause(requestContext)
	}
}

func (child *boundChild) requestDispose() {
	child.cancel()
	select {
	case child.detach <- struct{}{}:
	default:
	}
}

func (child *boundChild) operationContext(
	requestContext context.Context,
) (context.Context, context.CancelFunc) {
	workContext, cancelOperation := context.WithCancel(requestContext)
	stop := context.AfterFunc(child.ctx, cancelOperation)
	return workContext, func() {
		stop()
		cancelOperation()
	}
}

func (child *boundChild) initializeDelivery() {
	child.deliveryReady = true
}
