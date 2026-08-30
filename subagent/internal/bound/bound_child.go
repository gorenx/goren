package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	boundcontract "github.com/gorenx/goren/subagent/bound"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

type reconcileRequest struct {
	ctx   context.Context
	reply chan error
}

type followupCommand struct {
	ctx      context.Context
	message  agentmessage.UserMessage
	response chan followupResult
}

type deliveryCommand struct {
	ctx      context.Context
	input    boundcontract.Input
	response chan deliveryResult
}

type deliveryResult struct {
	receipt boundcontract.Receipt
	err     error
}

type followupResult struct {
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

// boundChild is the sole serial worker for one immutable user Session
// Binding. It outlives individual resident Agent epochs.
type boundChild struct {
	key          bindingKey
	parent       agent.Agent
	agents       agent.Registry
	sessions     session.LiveStore
	projections  sessionprojection.Registry
	definitions  *definitionCatalog
	publisher    sharedexecution.EventPublisher
	executions   *sharedexecution.Registry
	failures     FailureReporter
	materializer *materializer
	mailbox      chan any
	detach       chan struct{}
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	current      *residentEpoch
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
			parentID:       parentAgent.ID(),
			name:           bindingValue.Name,
			childSessionID: bindingValue.ChildSessionID,
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
		detach:       make(chan struct{}, 1),
		ctx:          childContext,
		cancel:       cancelChild,
		done:         make(chan struct{}),
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
		case <-child.detach:
			return
		}
	}
}

func (child *boundChild) handleCommand(received any) bool {
	switch commandValue := received.(type) {
	case reconcileRequest:
		_, err := child.reconcileExecution(commandValue.ctx)
		commandValue.reply <- err
	case followupCommand:
		identifier, err := child.handleFollowup(
			commandValue.ctx,
			commandValue.message,
		)
		commandValue.response <- followupResult{
			identifier: identifier,
			err:        err,
		}
	case deliveryCommand:
		receiptValue, err := child.handleDelivery(
			commandValue.ctx,
			commandValue.input,
		)
		commandValue.response <- deliveryResult{
			receipt: receiptValue,
			err:     err,
		}
	case interruptRequest:
		child.handleInterrupt()
		commandValue.reply <- struct{}{}
	case shutdownRequest:
		child.cancel()
		commandValue.reply <- child.stopCurrent(commandValue.ctx)
		return true
	}
	return false
}

func (child *boundChild) reconcile(requestContext context.Context) error {
	reply := make(chan error, 1)
	commandValue := reconcileRequest{
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

func (child *boundChild) followup(
	requestContext context.Context,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	response := make(chan followupResult, 1)
	commandValue := followupCommand{
		ctx:      requestContext,
		message:  messageValue,
		response: response,
	}
	select {
	case child.mailbox <- commandValue:
	case <-requestContext.Done():
		return "", context.Cause(requestContext)
	case <-child.done:
		return "", errors.New("subagent: Bound child is closed")
	}
	select {
	case outcome := <-response:
		return outcome.identifier, outcome.err
	case <-requestContext.Done():
		return "", context.Cause(requestContext)
	}
}

func (child *boundChild) deliver(
	requestContext context.Context,
	inputValue boundcontract.Input,
) (boundcontract.Receipt, error) {
	response := make(chan deliveryResult, 1)
	commandValue := deliveryCommand{
		ctx:      requestContext,
		input:    inputValue,
		response: response,
	}
	select {
	case child.mailbox <- commandValue:
	case <-requestContext.Done():
		return boundcontract.Receipt{}, context.Cause(requestContext)
	case <-child.done:
		return boundcontract.Receipt{}, errors.New(
			"subagent: Bound child is closed",
		)
	}
	select {
	case delivered := <-response:
		return delivered.receipt, delivered.err
	case <-requestContext.Done():
		return boundcontract.Receipt{}, context.Cause(requestContext)
	case <-child.done:
		return boundcontract.Receipt{}, errors.New(
			"subagent: Bound child is closed",
		)
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
	child.requestDispose()
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
	cancelWatch := context.AfterFunc(child.ctx, cancelOperation)
	return workContext, func() {
		cancelWatch()
		cancelOperation()
	}
}
