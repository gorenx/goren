package agent

import (
	"context"

	"github.com/gorenx/goren/session"
)

// CreateOptions contains the shared Agent/Session identity and unpublished
// Agent composition applied before publication.
type CreateOptions struct {
	SessionID     session.SessionID
	Metadata      session.Metadata
	Seed          []session.Event
	AgentOptions  Options
	Provisioner   Provisioner
	RuntimeParent Agent
}

// ResumeOptions contains durable identity and unpublished Agent composition.
type ResumeOptions struct {
	SessionID     session.SessionID
	AgentOptions  Options
	Provisioner   Provisioner
	RuntimeParent Agent
}

// Reservation is one unpublished Agent lifecycle slot owned by the Registry.
// The Factory attaches exactly one Agent and Scope before publication.
type Reservation interface {
	ClosingSignal() <-chan struct{}
	Attach(Agent, AgentScopeRuntime) (Lifecycle, error)
}

// Lifecycle represents one exact Agent instance from attachment through
// teardown. LifecycleCoordinator owns its state transitions.
type Lifecycle interface {
	BeginTeardown(context.Context)
	FinishTeardown(error)
}

// Factory is the Registry-owned construction seam implemented by Agent Loop.
type Factory interface {
	CreateAgent(context.Context, Reservation, CreateOptions) error
	ResumeAgent(context.Context, Reservation, ResumeOptions) error
}
