package subagent

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// ExtensionRegistration owns one exact Activation Extension registration and
// its resident installations.
type ExtensionRegistration interface {
	Unregister(context.Context) error
}

// ActivationContext is the immutable input for extending one unpublished
// continuable child Activation.
type ActivationContext struct {
	ChildID    session.SessionID
	ParentID   session.SessionID
	Scope      agent.Scope
	Descriptor ContinuableDescriptor
}

// Installation owns one exact idempotent Extension effect in one Activation.
type Installation interface {
	Uninstall(context.Context) error
}

// ActivationExtension installs one domain contribution into an unpublished
// continuable child Scope. The Extension may mount a child-scoped Plugin but
// does not itself participate in the Plugin lifecycle.
type ActivationExtension interface {
	Install(context.Context, ActivationContext) (Installation, error)
}

// ExtensionRegistry owns deployment extensions installed into unpublished
// and resident continuable children.
type ExtensionRegistry interface {
	plugin.Service
	RegisterExtension(ActivationExtension) (ExtensionRegistration, error)
}
