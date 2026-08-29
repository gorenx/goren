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

// AgentEpoch is the Registry-owned exact Agent lifecycle instance passed to a
// Factory before publication. The Factory attaches one Agent and Scope runtime
// to that same epoch; it does not construct a second lifecycle object.
type AgentEpoch interface {
	ClosingSignal() <-chan struct{}
	Attach(Agent, AgentScopeRuntime) (AgentTeardown, error)
}

// AgentTeardown reports structural Scope teardown into the exact Agent epoch
// owned by LifecycleCoordinator. It does not expose construction or close
// authority to the runtime adapter.
type AgentTeardown interface {
	BeginTeardown(context.Context)
	FinishTeardown(error)
}

// Factory is the Registry-owned construction seam implemented by Agent Loop.
type Factory interface {
	CreateAgent(context.Context, AgentEpoch, CreateOptions) error
	ResumeAgent(context.Context, AgentEpoch, ResumeOptions) error
}
