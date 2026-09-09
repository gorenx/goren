package tools

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"
)

// RegistrationID identifies one local execution middleware or result observer.
// Removal is serialized by the layer owner, independently of active executions.
type RegistrationID string

// UseExecution registers a middleware for subsequently prepared calls.
func (owner *ToolLayer) UseExecution(middleware ExecuteMiddleware) (RegistrationID, error) {
	if err := owner.requireActive(); err != nil {
		return "", err
	}
	if middleware == nil {
		return "", errors.New("tools: execution middleware is required")
	}
	identifier := RegistrationID(uuid.NewString())
	if owner.executions == nil {
		owner.executions = make(map[RegistrationID]ExecuteMiddleware)
	}
	owner.executions[identifier] = middleware
	owner.registrations = append(owner.registrations, identifier)
	owner.rebuildPipeline()
	return identifier, nil
}

// ObserveResults registers an observer for subsequently prepared calls.
func (owner *ToolLayer) ObserveResults(observer ResultObserver) (RegistrationID, error) {
	if err := owner.requireActive(); err != nil {
		return "", err
	}
	if observer == nil {
		return "", errors.New("tools: result observer is required")
	}
	identifier := RegistrationID(uuid.NewString())
	if owner.observers == nil {
		owner.observers = make(map[RegistrationID]ResultObserver)
	}
	owner.observers[identifier] = observer
	owner.registrations = append(owner.registrations, identifier)
	owner.rebuildPipeline()
	return identifier, nil
}

// RemoveRegistration removes only the named binding. Captured calls retain it.
func (owner *ToolLayer) RemoveRegistration(ctx context.Context, identifier RegistrationID) (bool, error) {
	if err := validateMutationContext(ctx); err != nil {
		return false, err
	}
	index := slices.Index(owner.registrations, identifier)
	if index < 0 {
		return false, nil
	}
	delete(owner.executions, identifier)
	delete(owner.observers, identifier)
	owner.registrations = slices.Delete(owner.registrations, index, index+1)
	owner.rebuildPipeline()
	return true, nil
}

func (owner *ToolLayer) rebuildPipeline() {
	captured := &pipeline{
		options: owner.pipeline.options,
	}
	for _, identifier := range owner.registrations {
		if middleware, found := owner.executions[identifier]; found {
			captured.executions = append(captured.executions, middleware)
		}
		if observer, found := owner.observers[identifier]; found {
			captured.observers = append(captured.observers, observer)
		}
	}
	owner.pipeline = captured
}
