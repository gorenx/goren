package middleware

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
)

// RequestEffect is a named dispatch or return from a request call.
type RequestEffect interface{ requestEffect() }

// InvokeRequest selects a definition without carrying an executable continuation.
type InvokeRequest struct {
	Handler RegistrationID
	Input   agent.RequestInput
}

// ReturnRequest returns to To; an empty To selects the original caller.
// Err remains a typed error that an upstream middleware may handle.
type ReturnRequest struct {
	To     RegistrationID
	Config llm.CallConfig
	Err    error
}

func (InvokeRequest) requestEffect() {}
func (ReturnRequest) requestEffect() {}

// Request owns one fixed around call. Its caller serializes methods and performs I/O.
type Request struct {
	order    chain
	input    agent.RequestInput
	terminal llm.CallConfig
}

// NewRequest captures detached inputs and an immutable registration order.
func NewRequest(order []RegistrationID, input agent.RequestInput, terminal llm.CallConfig) (*Request, error) {
	if input.AgentID == "" || input.Turn <= 0 || input.Step <= 0 || input.Turn > 1<<53-1 || input.Step > 1<<53-1 {
		return nil, errors.New("agent middleware: invalid Agent or Step position")
	}
	selection, err := newChain(order)
	if err != nil {
		return nil, err
	}
	copied, err := cloneRequest(input)
	if err != nil {
		return nil, err
	}

	fallback, err := cloneConfig(terminal)
	if err != nil {
		return nil, err
	}
	return &Request{
		order:    selection,
		input:    copied,
		terminal: fallback,
	}, nil
}

// Start invokes the first definition, or returns the default for an empty chain.
func (call *Request) Start() (RequestEffect, error) {
	target, err := call.order.start()
	if err != nil {
		return nil, err
	}
	return call.deliver(target)
}

// Continue requests the downstream suffix once, preserving the original input.
func (call *Request) Continue(identity RegistrationID) (RequestEffect, error) {
	target, err := call.order.proceed(identity)
	if err != nil {
		return nil, err
	}
	return call.deliver(target)
}

// Complete returns a short-circuit or post-downstream outcome to its caller.
func (call *Request) Complete(identity RegistrationID, result llm.CallConfig, failure error) (RequestEffect, error) {
	parent, err := call.order.finish(identity)
	if err != nil {
		return nil, err
	}
	if call.order.cause != nil {
		failure = call.order.cause
	}
	var detached llm.CallConfig
	if failure == nil {
		detached, failure = cloneConfig(result)
	}
	return ReturnRequest{
		To:     parent,
		Config: detached,
		Err:    failure,
	}, nil
}

func (call *Request) deliver(target delivery) (RequestEffect, error) {
	if target.invoke {
		copied, err := cloneRequest(call.input)
		if err != nil {
			return call.Complete(target.handler, llm.CallConfig{}, err)
		}
		return InvokeRequest{
			Handler: target.handler,
			Input:   copied,
		}, nil
	}
	fallback, err := cloneConfig(call.terminal)
	if call.order.cause != nil {
		err = call.order.cause
	}
	return ReturnRequest{
		To:     target.handler,
		Config: fallback,
		Err:    err,
	}, nil
}

// Cancel prevents new downstream work and returns the first cancellation cause.
// Adapters forward this value so every module observes the same cancellation.
func (call *Request) Cancel(cause error) error {
	if cause != nil && call.order.cause == nil {
		call.order.cause = cause
	}
	return call.order.cause
}
