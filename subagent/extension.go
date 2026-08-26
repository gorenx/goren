package subagent

import (
	"context"

	"github.com/gorenx/goren/agent"
)

// ExtensionRegistration owns one exact child Extension registration and its
// resident installations.
type ExtensionRegistration interface {
	Unregister(context.Context) error
}

// ExtensionInstallation owns one exact idempotent child-scoped effect.
type ExtensionInstallation interface {
	Uninstall(context.Context) error
}

// Extension installs one domain contribution into an unpublished child Agent
// Scope. The Extension may mount a child-scoped Plugin but does not itself
// participate in the Plugin lifecycle.
type Extension interface {
	Install(context.Context, agent.Scope) (ExtensionInstallation, error)
}

// ExtensionRegistry owns Extensions installed into unpublished and resident
// child Agents.
type ExtensionRegistry interface {
	RegisterExtension(Extension) (ExtensionRegistration, error)
}
