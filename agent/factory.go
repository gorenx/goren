package agent

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
)

// CreateOptions contains the shared Agent/Session identity and unpublished
// Agent composition applied before publication.
type CreateOptions struct {
	SessionID    session.SessionID
	Metadata     session.Metadata
	Seed         []session.Event
	AgentOptions Options
	Provisioner  Provisioner
}

// ResumeOptions contains durable identity and unpublished Agent composition.
type ResumeOptions struct {
	SessionID    session.SessionID
	AgentOptions Options
	Provisioner  Provisioner
}

// AgentLifecycle owns teardown of one exact Agent tree.
type AgentLifecycle interface {
	Dispose(context.Context) error
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

// Dispose stops and removes the exact Agent lifecycle owned by this Handle.
func (owned Handle) Dispose(closeContext context.Context) error {
	if owned.Lifecycle == nil {
		return nil
	}
	return owned.Lifecycle.Dispose(closeContext)
}

// Factory is the Registry-owned construction seam implemented by Agent Loop.
type Factory interface {
	CreateAgent(context.Context, CreateOptions) (Handle, error)
	ResumeAgent(context.Context, ResumeOptions) (Handle, error)
}
