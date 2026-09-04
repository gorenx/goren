package agentloop

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
)

// agentEvents publishes events from one exact ReactLoopAgent Scope.
type agentEvents interface {
	Dispatch(context.Context, agent.AgentEvent) error
}

// agentWaterfalls executes the three Agent extension chains used by RLA.
type agentWaterfalls interface {
	ResolvePreStep(
		context.Context,
		agent.PreStepNotice,
		agent.PreStepAction,
	) (agent.PreStepDecision, error)
	ResolveRequest(
		context.Context,
		agent.RequestNotice,
		agent.RequestAction,
	) (agent.RequestResolution, error)
	ResolveRequestError(
		context.Context,
		agent.RequestErrorNotice,
		agent.RequestErrorHandler,
	) (agent.RequestErrorAction, error)
}

type effectCall interface {
	effectCall()
}

type eventCall struct {
	ctx    context.Context
	fact   agent.AgentEvent
	result chan error
}

func (eventCall) effectCall() {}

type preStepCall struct {
	ctx      context.Context
	notice   agent.PreStepNotice
	terminal agent.PreStepAction
	result   chan preStepCallResult
}

func (preStepCall) effectCall() {}

type preStepCallResult struct {
	decision agent.PreStepDecision
	err      error
}

type requestResolutionCall struct {
	ctx      context.Context
	notice   agent.RequestNotice
	terminal agent.RequestAction
	result   chan requestResolutionCallResult
}

func (requestResolutionCall) effectCall() {}

type requestResolutionCallResult struct {
	resolution agent.RequestResolution
	err        error
}

type requestErrorCall struct {
	ctx      context.Context
	notice   agent.RequestErrorNotice
	terminal agent.RequestErrorHandler
	result   chan requestErrorCallResult
}

func (requestErrorCall) effectCall() {}

type requestErrorCallResult struct {
	action agent.RequestErrorAction
	err    error
}

// pluginGateway submits Event and Waterfall requests to the sole AgentLoop
// Plugin. It contains no Plugin, Runtime, callback, or owner reference.
type pluginGateway struct {
	requests chan effectCall
	stopped  chan struct{}
	stopOnce sync.Once
	// stopErr is published before stopped closes and read only after a stopped
	// receive establishes the lifecycle handoff.
	stopErr error
}

func newPluginGateway() *pluginGateway {
	return &pluginGateway{
		requests: make(chan effectCall),
		stopped:  make(chan struct{}),
	}
}

func (gateway *pluginGateway) submit(
	ctx context.Context,
	call effectCall,
) error {
	if gateway == nil || ctx == nil || call == nil {
		return errors.New("agentloop: effect call is incomplete")
	}
	select {
	case gateway.requests <- call:
		return nil
	case <-gateway.stopped:
		return gateway.stoppedError()
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (gateway *pluginGateway) Dispatch(
	ctx context.Context,
	fact agent.AgentEvent,
) error {
	result := make(chan error, 1)
	if err := gateway.submit(
		ctx,
		eventCall{
			ctx:    ctx,
			fact:   fact,
			result: result,
		},
	); err != nil {
		return err
	}
	select {
	case err := <-result:
		return err
	case <-gateway.stopped:
		return gateway.stoppedError()
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (gateway *pluginGateway) ResolvePreStep(
	ctx context.Context,
	notice agent.PreStepNotice,
	terminal agent.PreStepAction,
) (agent.PreStepDecision, error) {
	result := make(chan preStepCallResult, 1)
	if err := gateway.submit(
		ctx,
		preStepCall{
			ctx:      ctx,
			notice:   notice,
			terminal: terminal,
			result:   result,
		},
	); err != nil {
		return agent.PreStepDecision{}, err
	}
	select {
	case resolved := <-result:
		return resolved.decision, resolved.err
	case <-gateway.stopped:
		return agent.PreStepDecision{}, gateway.stoppedError()
	case <-ctx.Done():
		return agent.PreStepDecision{}, context.Cause(ctx)
	}
}

func (gateway *pluginGateway) ResolveRequest(
	ctx context.Context,
	notice agent.RequestNotice,
	terminal agent.RequestAction,
) (agent.RequestResolution, error) {
	result := make(chan requestResolutionCallResult, 1)
	if err := gateway.submit(
		ctx,
		requestResolutionCall{
			ctx:      ctx,
			notice:   notice,
			terminal: terminal,
			result:   result,
		},
	); err != nil {
		return agent.RequestResolution{}, err
	}
	select {
	case resolved := <-result:
		return resolved.resolution, resolved.err
	case <-gateway.stopped:
		return agent.RequestResolution{}, gateway.stoppedError()
	case <-ctx.Done():
		return agent.RequestResolution{}, context.Cause(ctx)
	}
}

func (gateway *pluginGateway) ResolveRequestError(
	ctx context.Context,
	notice agent.RequestErrorNotice,
	terminal agent.RequestErrorHandler,
) (agent.RequestErrorAction, error) {
	result := make(chan requestErrorCallResult, 1)
	if err := gateway.submit(
		ctx,
		requestErrorCall{
			ctx:      ctx,
			notice:   notice,
			terminal: terminal,
			result:   result,
		},
	); err != nil {
		return agent.RequestErrorAction{}, err
	}
	select {
	case resolved := <-result:
		return resolved.action, resolved.err
	case <-gateway.stopped:
		return agent.RequestErrorAction{}, gateway.stoppedError()
	case <-ctx.Done():
		return agent.RequestErrorAction{}, context.Cause(ctx)
	}
}

func (gateway *pluginGateway) stop(cause error) {
	if gateway == nil {
		return
	}
	gateway.stopOnce.Do(func() {
		if cause == nil {
			cause = errors.New("agentloop: effect client stopped")
		}
		gateway.stopErr = cause
		close(gateway.stopped)
	})
}

func (gateway *pluginGateway) stoppedError() error {
	return gateway.stopErr
}

var _ agentEvents = (*pluginGateway)(nil)
var _ agentWaterfalls = (*pluginGateway)(nil)
