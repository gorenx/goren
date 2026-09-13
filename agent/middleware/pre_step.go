package middleware

import (
	"errors"

	"github.com/gorenx/goren/agent"
)

// PreStepEffect is a named dispatch or return from a pre-step call.
type PreStepEffect interface{ prestepEffect() }

// InvokePreStep selects a definition without carrying an executable continuation.
type InvokePreStep struct {
	Handler RegistrationID
	Input   agent.PreStepInput
}

// ReturnPreStep returns to To; an empty To selects the original caller.
// Err remains a typed error that an upstream middleware may handle.
type ReturnPreStep struct {
	To       RegistrationID
	Decision agent.PreStepDecision
	Err      error
}

func (InvokePreStep) prestepEffect() {}
func (ReturnPreStep) prestepEffect() {}

// PreStep owns one fixed around call. Its caller serializes methods and performs I/O.
type PreStep struct {
	order    chain
	input    agent.PreStepInput
	terminal agent.PreStepDecision
}

// NewPreStep captures detached inputs and an immutable registration order.
func NewPreStep(order []RegistrationID, input agent.PreStepInput, terminal agent.PreStepDecision) (*PreStep, error) {
	if input.AgentID == "" ||
		input.Turn <= 0 ||
		input.Step <= 0 ||
		input.Turn > 1<<53-1 ||
		input.Step > 1<<53-1 {
		return nil, errors.New("agent middleware: invalid Agent or Step position")
	}
	selection, err := newChain(order)
	if err != nil {
		return nil, err
	}
	copied, err := clonePreStep(input)
	if err != nil {
		return nil, err
	}

	fallback, err := cloneDecision(terminal)
	if err != nil {
		return nil, err
	}
	return &PreStep{
		order:    selection,
		input:    copied,
		terminal: fallback,
	}, nil
}

// Start invokes the first definition, or returns the default for an empty chain.
func (call *PreStep) Start() (PreStepEffect, error) {
	target, err := call.order.start()
	if err != nil {
		return nil, err
	}
	return call.deliver(target)
}

// Continue requests the downstream suffix once, preserving the original input.
func (call *PreStep) Continue(identity RegistrationID) (PreStepEffect, error) {
	target, err := call.order.proceed(identity)
	if err != nil {
		return nil, err
	}
	return call.deliver(target)
}

// Complete returns a short-circuit or post-downstream outcome to its caller.
func (call *PreStep) Complete(identity RegistrationID, result agent.PreStepDecision, failure error) (PreStepEffect, error) {
	parent, err := call.order.finish(identity)
	if err != nil {
		return nil, err
	}
	if call.order.cause != nil {
		failure = call.order.cause
	}
	var detached agent.PreStepDecision
	if failure == nil {
		detached, failure = cloneDecision(result)
	}
	return ReturnPreStep{
		To:       parent,
		Decision: detached,
		Err:      failure,
	}, nil
}

func (call *PreStep) deliver(target delivery) (PreStepEffect, error) {
	if target.invoke {
		copied, err := clonePreStep(call.input)
		if err != nil {
			return call.Complete(target.handler, agent.PreStepDecision{}, err)
		}
		return InvokePreStep{
			Handler: target.handler,
			Input:   copied,
		}, nil
	}
	fallback, err := cloneDecision(call.terminal)
	if call.order.cause != nil {
		err = call.order.cause
	}
	return ReturnPreStep{
		To:       target.handler,
		Decision: fallback,
		Err:      err,
	}, nil
}

// Cancel prevents new downstream work and returns the first cancellation cause.
// Adapters forward this value so every module observes the same cancellation.
func (call *PreStep) Cancel(cause error) error {
	if cause != nil && call.order.cause == nil {
		call.order.cause = cause
	}
	return call.order.cause
}
