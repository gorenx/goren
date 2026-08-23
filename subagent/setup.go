package subagent

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// SetupRegistration owns one exact continuable setup registration and its
// resident installations.
type SetupRegistration interface {
	Unregister(context.Context) error
}

// ActivationContext is the immutable input for composing one unpublished
// continuable child activation.
type ActivationContext struct {
	ChildID    session.SessionID
	ParentID   session.SessionID
	Agent      agent.Agent
	Descriptor ContinuableDescriptor
}

// Installation owns one exact idempotent Setup effect in one Activation.
type Installation interface {
	Uninstall(context.Context) error
}

// Setup installs one domain contribution into an unpublished continuable
// child. It is not a Plugin and does not own a Service scope.
type Setup interface {
	Install(context.Context, ActivationContext) (Installation, error)
}

// SetupRegistry owns deployment contributions installed into unpublished and
// resident continuable children.
type SetupRegistry interface {
	plugin.Service
	RegisterContinuableSetup(Setup) (SetupRegistration, error)
}
