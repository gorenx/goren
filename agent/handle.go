package agent

import (
	"context"
)

type managedLifecycle interface {
	Dispose(context.Context) error
	ClosingSignal() <-chan struct{}
}

// Handle is the exact live Agent and its lifecycle owner.
type Handle struct {
	Subject   Agent
	lifecycle managedLifecycle
}

// ClosingSignal closes when explicit or structural Handle teardown starts.
func (owned Handle) ClosingSignal() <-chan struct{} {
	if owned.lifecycle == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return owned.lifecycle.ClosingSignal()
}

// Dispose stops and removes the exact Agent lifecycle owned by this Handle.
func (owned Handle) Dispose(closeContext context.Context) error {
	if owned.lifecycle == nil {
		return nil
	}
	return owned.lifecycle.Dispose(closeContext)
}
