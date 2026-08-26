package continuation

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/subagent"
)

// Service is the stable continuable capability published by the Subagent
// Plugin. A Manager exists only while the optional Agent and Session
// dependencies are available.
type Service struct {
	mutex  sync.RWMutex
	active *Manager
}

// NewService constructs a disabled continuable Service.
func NewService() *Service {
	return &Service{}
}

// Enable installs the Manager created for the current Plugin activation.
func (owner *Service) Enable(activeManager *Manager) error {
	if activeManager == nil {
		return errors.New("subagent: cannot enable continuation with a nil Manager")
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.active != nil {
		return errors.New("subagent: continuation Service is already enabled")
	}
	owner.active = activeManager
	return nil
}

// Disable closes admission, detaches the current Manager, and waits until each
// resident Activation has submitted an exact managed close request. Agent
// lifecycle completes structural teardown after the current Plugin operation.
func (owner *Service) Disable(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	owner.mutex.Lock()
	activeManager := owner.active
	owner.active = nil
	owner.mutex.Unlock()
	if activeManager == nil {
		return nil
	}
	return activeManager.RequestClose(context.WithoutCancel(closeContext))
}

func (owner *Service) requireManager() (*Manager, error) {
	owner.mutex.RLock()
	activeManager := owner.active
	owner.mutex.RUnlock()
	if activeManager == nil {
		return nil, &subagent.Error{
			Code: subagent.ErrorContinuationUnavailable,
			Message: "continuable subagents require the Agent Registry and " +
				"Session LiveStore",
		}
	}
	return activeManager, nil
}
