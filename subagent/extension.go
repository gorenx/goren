package subagent

import (
	"context"

	"github.com/gorenx/goren/agent"
)

// ExtensionRegistration owns one exact Continuable Extension registration and
// its resident installations.
type ExtensionRegistration interface {
	Unregister(context.Context) error
}

// ExtensionInstallation owns one exact idempotent child-scoped effect.
type ExtensionInstallation interface {
	Uninstall(context.Context) error
}

// ContinuableExtension installs one domain contribution into an unpublished
// continuable child Scope. The Extension may mount a child-scoped Plugin but
// does not itself participate in the Plugin lifecycle.
type ContinuableExtension interface {
	Install(context.Context, agent.Scope) (ExtensionInstallation, error)
}

// ExtensionRegistry owns deployment extensions installed into unpublished
// and resident continuable children.
type ExtensionRegistry interface {
	RegisterExtension(ContinuableExtension) (ExtensionRegistration, error)
}
