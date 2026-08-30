package agent

import (
	"context"
)

// Handle is the exact live Agent and its lifecycle owner.
type Handle struct {
	Subject  Agent
	registry *RegistryService
	lifetime *agentLifetime
}

// ClosingSignal closes when explicit or structural Handle teardown starts.
func (owned Handle) ClosingSignal() <-chan struct{} {
	if owned.lifetime == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return owned.lifetime.ClosingSignal()
}

// Dispose stops and removes the exact Agent lifecycle owned by this Handle.
func (owned Handle) Dispose(closeContext context.Context) error {
	if owned.registry == nil || owned.lifetime == nil {
		return nil
	}
	return owned.registry.closeLifetime(closeContext, owned.lifetime)
}
