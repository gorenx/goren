package agent

import (
	"context"
	"errors"
)

// AgentLifecycle owns teardown of one exact Agent tree.
type AgentLifecycle interface {
	Dispose(context.Context) error
	ClosingSignal() <-chan struct{}
}

// Handle is the exact live Agent and its lifecycle owner.
type Handle struct {
	Subject   Agent
	Lifecycle AgentLifecycle
}

// NewHandle validates and creates one Agent Handle.
func NewHandle(subject Agent, lifecycle AgentLifecycle) (Handle, error) {
	if subject == nil || lifecycle == nil {
		return Handle{}, errors.New("agent: Handle requires an Agent and lifecycle")
	}
	return Handle{
		Subject:   subject,
		Lifecycle: lifecycle,
	}, nil
}

// ClosingSignal closes when explicit or structural Handle teardown starts.
func (owned Handle) ClosingSignal() <-chan struct{} {
	if owned.Lifecycle == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return owned.Lifecycle.ClosingSignal()
}

// Dispose stops and removes the exact Agent lifecycle owned by this Handle.
func (owned Handle) Dispose(closeContext context.Context) error {
	if owned.Lifecycle == nil {
		return nil
	}
	return owned.Lifecycle.Dispose(closeContext)
}
