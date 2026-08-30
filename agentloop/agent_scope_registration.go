package agentloop

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
)

// scopeRegistration is one ordered, removable Scope contribution. It has no
// owner reference, so registration resources cannot point back to Agent Scope.
type scopeRegistration[T any] struct {
	once   sync.Once
	mutex  sync.RWMutex
	value  T
	active bool
}

func newScopeRegistration[T any](value T) *scopeRegistration[T] {
	return &scopeRegistration[T]{
		value:  value,
		active: true,
	}
}

func (registration *scopeRegistration[T]) get() (T, bool) {
	registration.mutex.RLock()
	value := registration.value
	active := registration.active
	registration.mutex.RUnlock()
	return value, active
}

func (registration *scopeRegistration[T]) Close(context.Context) error {
	if registration == nil {
		return nil
	}
	registration.once.Do(func() {
		registration.mutex.Lock()
		var zero T
		registration.value = zero
		registration.active = false
		registration.mutex.Unlock()
	})
	return nil
}

func (owner *agentScope) observeAgentEvents(
	observer agent.AgentEventObserver,
) (*scopeRegistration[agent.AgentEventObserver], error) {
	if observer == nil {
		return nil, errors.New("agentloop: Agent event observer is nil")
	}
	registration := newScopeRegistration(observer)
	owner.mutex.Lock()
	owner.observers = append(owner.observers, registration)
	owner.mutex.Unlock()
	return registration, nil
}

func (owner *agentScope) usePreStep(
	middleware agent.PreStepMiddleware,
) (*scopeRegistration[agent.PreStepMiddleware], error) {
	if middleware == nil {
		return nil, errors.New("agentloop: pre-step Middleware is nil")
	}
	registration := newScopeRegistration(middleware)
	owner.mutex.Lock()
	owner.preStep = append(owner.preStep, registration)
	owner.mutex.Unlock()
	return registration, nil
}

func (owner *agentScope) useRequest(
	middleware agent.RequestMiddleware,
) (*scopeRegistration[agent.RequestMiddleware], error) {
	if middleware == nil {
		return nil, errors.New("agentloop: request Middleware is nil")
	}
	registration := newScopeRegistration(middleware)
	owner.mutex.Lock()
	owner.requests = append(owner.requests, registration)
	owner.mutex.Unlock()
	return registration, nil
}

func (owner *agentScope) useRequestError(
	middleware agent.RequestErrorMiddleware,
) (*scopeRegistration[agent.RequestErrorMiddleware], error) {
	if middleware == nil {
		return nil, errors.New("agentloop: request-error Middleware is nil")
	}
	registration := newScopeRegistration(middleware)
	owner.mutex.Lock()
	owner.requestError = append(owner.requestError, registration)
	owner.mutex.Unlock()
	return registration, nil
}

func (owner *agentScope) Dispatch(
	requestContext context.Context,
	fact agent.AgentEvent,
) error {
	owner.mutex.RLock()
	registrations := append(
		[]*scopeRegistration[agent.AgentEventObserver](nil),
		owner.observers...,
	)
	owner.mutex.RUnlock()
	for _, registration := range registrations {
		observer, active := registration.get()
		if !active {
			continue
		}
		if err := observer.ObserveAgentEvent(requestContext, fact); err != nil {
			return err
		}
	}
	return owner.events.Dispatch(requestContext, fact)
}
