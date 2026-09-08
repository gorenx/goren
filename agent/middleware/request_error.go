package middleware

import (
	"errors"
	"github.com/gorenx/goren/agent"
)

// RequestErrorEffect is a named dispatch or return from a request-error call.
type RequestErrorEffect interface{ requesterrorEffect() }

// InvokeRequestError selects a definition without carrying an executable continuation.
type InvokeRequestError struct {
	Handler RegistrationID
	Input   agent.RequestFailure
}

// ReturnRequestError returns to To; an empty To selects the original caller.
// Err remains a typed error that an upstream middleware may handle.
type ReturnRequestError struct {
	To     RegistrationID
	Action agent.RequestErrorAction
	Err    error
}

func (InvokeRequestError) requesterrorEffect() {}
func (ReturnRequestError) requesterrorEffect() {}

// RequestError owns one fixed around call. Its caller serializes methods and performs I/O.
type RequestError struct {
	order    chain
	input    agent.RequestFailure
	terminal agent.RequestErrorAction
}

// NewRequestError captures detached inputs and an immutable registration order.
func NewRequestError(order []RegistrationID, input agent.RequestFailure) (*RequestError, error) {
	if input.AgentID == "" || input.Turn <= 0 || input.Step <= 0 || input.Turn > 1<<53-1 || input.Step > 1<<53-1 {
		return nil, errors.New("agent middleware: invalid Agent or Step position")
	}
	selection, err := newChain(order)
	if err != nil {
		return nil, err
	}
	copied, err := cloneFailure(input)
	if err != nil {
		return nil, err
	}
	terminal := agent.RequestErrorAction{}
	fallback, err := cloneAction(terminal)
	if err != nil {
		return nil, err
	}
	return &RequestError{
		order:    selection,
		input:    copied,
		terminal: fallback,
	}, nil
}

// Start invokes the first definition, or returns the default for an empty chain.
func (call *RequestError) Start() (RequestErrorEffect, error) {
	target, err := call.order.start()
	if err != nil {
		return nil, err
	}
	return call.deliver(target)
}

// Continue requests the downstream suffix once, preserving the original input.
func (call *RequestError) Continue(identity RegistrationID) (RequestErrorEffect, error) {
	target, err := call.order.proceed(identity)
	if err != nil {
		return nil, err
	}
	return call.deliver(target)
}

// Complete returns a short-circuit or post-downstream outcome to its caller.
func (call *RequestError) Complete(identity RegistrationID, result agent.RequestErrorAction, failure error) (RequestErrorEffect, error) {
	parent, err := call.order.finish(identity)
	if err != nil {
		return nil, err
	}
	if call.order.cause != nil {
		failure = call.order.cause
	}
	var detached agent.RequestErrorAction
	if failure == nil {
		detached, failure = cloneAction(result)
	}
	return ReturnRequestError{
		To:     parent,
		Action: detached,
		Err:    failure,
	}, nil
}

func (call *RequestError) deliver(target delivery) (RequestErrorEffect, error) {
	if target.invoke {
		copied, err := cloneFailure(call.input)
		if err != nil {
			return call.Complete(target.handler, agent.RequestErrorAction{}, err)
		}
		return InvokeRequestError{
			Handler: target.handler,
			Input:   copied,
		}, nil
	}
	fallback, err := cloneAction(call.terminal)
	if call.order.cause != nil {
		err = call.order.cause
	}
	return ReturnRequestError{
		To:     target.handler,
		Action: fallback,
		Err:    err,
	}, nil
}

// Cancel prevents new downstream work and returns the first cancellation cause.
// Adapters forward this value so every module observes the same cancellation.
func (call *RequestError) Cancel(cause error) error {
	if cause != nil && call.order.cause == nil {
		call.order.cause = cause
	}
	return call.order.cause
}
