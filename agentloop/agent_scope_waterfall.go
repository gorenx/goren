package agentloop

import (
	"context"

	"github.com/gorenx/goren/agent"
)

func (owner *agentScope) ResolvePreStep(
	requestContext context.Context,
	notice agent.PreStepNotice,
	terminal agent.PreStepAction,
) (agent.PreStepDecision, error) {
	finishCall, err := owner.beginCall()
	if err != nil {
		return agent.PreStepDecision{}, err
	}
	defer finishCall()
	owner.mutex.RLock()
	registrations := append(
		[]*scopeRegistration[agent.PreStepMiddleware](nil),
		owner.preStep...,
	)
	owner.mutex.RUnlock()
	local := terminal
	for index := len(registrations) - 1; index >= 0; index-- {
		middleware, active := registrations[index].get()
		if !active {
			continue
		}
		local = preStepAction{
			middleware: middleware,
			next:       local,
		}
	}
	return owner.waterfalls.ResolvePreStep(requestContext, notice, local)
}

func (owner *agentScope) ResolveRequest(
	requestContext context.Context,
	notice agent.RequestNotice,
	terminal agent.RequestAction,
) (agent.RequestResolution, error) {
	finishCall, err := owner.beginCall()
	if err != nil {
		return agent.RequestResolution{}, err
	}
	defer finishCall()
	owner.mutex.RLock()
	registrations := append(
		[]*scopeRegistration[agent.RequestMiddleware](nil),
		owner.requests...,
	)
	owner.mutex.RUnlock()
	local := terminal
	for index := len(registrations) - 1; index >= 0; index-- {
		middleware, active := registrations[index].get()
		if !active {
			continue
		}
		local = requestAction{
			middleware: middleware,
			next:       local,
		}
	}
	return owner.waterfalls.ResolveRequest(requestContext, notice, local)
}

func (owner *agentScope) ResolveRequestError(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	terminal agent.RequestErrorHandler,
) (agent.RequestErrorAction, error) {
	finishCall, err := owner.beginCall()
	if err != nil {
		return agent.RequestErrorAction{}, err
	}
	defer finishCall()
	owner.mutex.RLock()
	registrations := append(
		[]*scopeRegistration[agent.RequestErrorMiddleware](nil),
		owner.requestError...,
	)
	owner.mutex.RUnlock()
	local := terminal
	for index := len(registrations) - 1; index >= 0; index-- {
		middleware, active := registrations[index].get()
		if !active {
			continue
		}
		local = requestErrorAction{
			middleware: middleware,
			next:       local,
		}
	}
	return owner.waterfalls.ResolveRequestError(requestContext, notice, local)
}

type preStepAction struct {
	middleware agent.PreStepMiddleware
	next       agent.PreStepAction
}

func (action preStepAction) Execute(
	requestContext context.Context,
	notice agent.PreStepNotice,
) (agent.PreStepDecision, error) {
	return action.middleware.InterceptPreStep(requestContext, notice, action.next)
}

type requestAction struct {
	middleware agent.RequestMiddleware
	next       agent.RequestAction
}

func (action requestAction) Execute(
	requestContext context.Context,
	notice agent.RequestNotice,
) (agent.RequestResolution, error) {
	return action.middleware.InterceptRequest(requestContext, notice, action.next)
}

type requestErrorAction struct {
	middleware agent.RequestErrorMiddleware
	next       agent.RequestErrorHandler
}

func (action requestErrorAction) Execute(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
) (agent.RequestErrorAction, error) {
	return action.middleware.InterceptRequestError(requestContext, notice, action.next)
}
