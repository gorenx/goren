package agent

import (
	"context"
)

// Handle is the exact live Agent and its lifecycle owner.
type Handle struct {
	Subject  Agent
	registry *RegistryService
	record   *agentRecord
}

// ClosingSignal closes when explicit or structural Handle teardown starts.
func (owned Handle) ClosingSignal() <-chan struct{} {
	if owned.record == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return owned.record.closing.done
}

// Dispose stops and removes the exact Agent lifecycle owned by this Handle.
func (owned Handle) Dispose(closeContext context.Context) error {
	if owned.registry == nil || owned.record == nil {
		return nil
	}
	return owned.registry.closeRecord(closeContext, owned.record)
}
