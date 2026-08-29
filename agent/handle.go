package agent

import (
	"context"
)

// Handle is the exact live Agent and its lifecycle owner.
type Handle struct {
	Subject     Agent
	coordinator *LifecycleCoordinator
	epoch       *epoch
}

// ClosingSignal closes when explicit or structural Handle teardown starts.
func (owned Handle) ClosingSignal() <-chan struct{} {
	if owned.epoch == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return owned.epoch.closing.done
}

// Dispose stops and removes the exact Agent lifecycle owned by this Handle.
func (owned Handle) Dispose(closeContext context.Context) error {
	if owned.coordinator == nil || owned.epoch == nil {
		return nil
	}
	return owned.coordinator.closeExact(closeContext, owned.epoch)
}
