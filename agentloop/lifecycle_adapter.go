package agentloop

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
)

// lifecycleAdapter translates Plugin disposal into the beginning of Agent
// lifecycle teardown before the Scope event source is withdrawn.
type lifecycleAdapter struct {
	plugin.Base
	lifecycle agent.Lifecycle
}

func (*lifecycleAdapter) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName + "/lifecycle-adapter",
	}
}

func (*lifecycleAdapter) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (adapter *lifecycleAdapter) Dispose(closeContext context.Context) error {
	if adapter.lifecycle != nil {
		adapter.lifecycle.BeginTeardown(closeContext)
	}
	return nil
}

var _ plugin.Plugin = (*lifecycleAdapter)(nil)
