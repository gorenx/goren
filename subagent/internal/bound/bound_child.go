package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

type startRequest struct {
	ctx   context.Context
	reply chan startResult
}

type startResult struct {
	execution subagent.Execution
	err       error
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

type configRequest struct {
	ctx              context.Context
	expectedRevision int64
	config           subagent.BoundConfigSnapshot
	reply            chan configResult
}

type configResult struct {
	value subagent.UpdateBoundConfigResult
	err   error
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

// boundChild is one Bound child owned by an exact parent Agent epoch. It owns
// the child command order, resident Execution, delivery floor, and
// materialization transitions.
type boundChild struct {
	key           boundChildKey
	parent        agent.Agent
	agents        agent.Registry
	sessions      session.LiveStore
	projections   sessionprojection.Registry
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
	dependencySet Dependencies,
	factory *materializer,
	parentAgent agent.Agent,
	childID session.SessionID,
) *boundChild {
	childContext, cancelChild := context.WithCancel(context.Background())
	return &boundChild{
		key: boundChildKey{
			parentID: parentAgent.ID(),
			childID:  childID,
		},
		parent:       parentAgent,
		agents:       dependencySet.Agents,
		sessions:     dependencySet.Sessions,
		projections:  dependencySet.Projections,
		publisher:    dependencySet.Publisher,
		executions:   dependencySet.Executions,
		failures:     dependencySet.Failures,
		materializer: factory,
		mailbox:      make(chan any),
		dirty:        make(chan struct{}, 1),
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
	switch request := received.(type) {
	case startRequest:
		execution, err := child.handleStart(request.ctx)
		request.reply <- startResult{
			execution: execution,
			err:       err,
		}
	case messageRequest:
		identifier, err := child.handleMessage(request.ctx, request.message)
		request.reply <- messageResult{
			identifier: identifier,
			err:        err,
		}
	case configRequest:
		value, err := child.handleConfig(
			request.ctx,
			request.expectedRevision,
			request.config,
		)
		request.reply <- configResult{
			value: value,
			err:   err,
		}
	case interruptRequest:
		child.handleInterrupt()
		request.reply <- struct{}{}
	case catchUpRequest:
		request.reply <- child.catchUpInteractions()
	case shutdownRequest:
		child.cancel()
		request.reply <- child.stopCurrent(request.ctx)
		return true
	case executionClosedNotice:
		if child.current == request.epoch {
			child.current = nil
		}
	}
	return false
}

func (child *boundChild) start(
	ctx context.Context,
) (subagent.Execution, error) {
	reply := make(chan startResult, 1)
	request := startRequest{
		ctx:   ctx,
		reply: reply,
	}
	select {
	case child.mailbox <- request:
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-child.done:
		return nil, errors.New("subagent: Bound child is closed")
	}
	select {
	case result := <-reply:
		return result.execution, result.err
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

func (child *boundChild) send(
	ctx context.Context,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	reply := make(chan messageResult, 1)
	request := messageRequest{
		ctx:     ctx,
		message: messageValue,
		reply:   reply,
	}
	select {
	case child.mailbox <- request:
	case <-ctx.Done():
		return "", context.Cause(ctx)
	case <-child.done:
		return "", errors.New("subagent: Bound child is closed")
	}
	select {
	case result := <-reply:
		return result.identifier, result.err
	case <-ctx.Done():
		return "", context.Cause(ctx)
	}
}

func (child *boundChild) updateConfig(
	ctx context.Context,
	expectedRevision int64,
	config subagent.BoundConfigSnapshot,
) (subagent.UpdateBoundConfigResult, error) {
	reply := make(chan configResult, 1)
	request := configRequest{
		ctx:              ctx,
		expectedRevision: expectedRevision,
		config:           config,
		reply:            reply,
	}
	select {
	case child.mailbox <- request:
	case <-ctx.Done():
		return subagent.UpdateBoundConfigResult{}, context.Cause(ctx)
	case <-child.done:
		return subagent.UpdateBoundConfigResult{}, errors.New(
			"subagent: Bound child is closed",
		)
	}
	select {
	case result := <-reply:
		return result.value, result.err
	case <-ctx.Done():
		return subagent.UpdateBoundConfigResult{}, context.Cause(ctx)
	}
}

func (child *boundChild) interrupt(ctx context.Context) error {
	reply := make(chan struct{}, 1)
	request := interruptRequest{
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
	case <-reply:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (child *boundChild) catchUp(ctx context.Context) error {
	reply := make(chan error, 1)
	request := catchUpRequest{
		reply: reply,
	}
	select {
	case child.mailbox <- request:
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-child.done:
		return errors.New("subagent: Bound child is closed")
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (child *boundChild) notify() {
	select {
	case child.dirty <- struct{}{}:
	default:
	}
}

func (child *boundChild) executionClosed(closed *residentEpoch) {
	go func() {
		select {
		case child.mailbox <- executionClosedNotice{
			epoch: closed,
		}:
		case <-child.done:
		}
	}()
}

func (child *boundChild) shutdown(ctx context.Context) error {
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

func (child *boundChild) dispose(ctx context.Context) error {
	child.cancel()
	select {
	case child.detach <- struct{}{}:
	case <-child.done:
		return nil
	}
	select {
	case <-child.done:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
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

func (child *boundChild) initializeDelivery(
	binding subagentprojection.BoundBinding,
	childSession session.Context,
) {
	if child.deliveryReady {
		return
	}
	child.floor = binding.Seq + 1
	if seedLength := childSession.Header().SeedLength; seedLength != nil &&
		*seedLength > child.floor {
		child.floor = *seedLength
	}
	child.deliveryReady = true
}
