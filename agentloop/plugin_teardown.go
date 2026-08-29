package agentloop

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
)

// teardownAdapter translates Plugin disposal into the beginning of structural
// teardown before the Scope event source is withdrawn.
type teardownAdapter struct {
	plugin.Base
	teardown agent.AgentTeardown
}

func (*teardownAdapter) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName + "/teardown-adapter",
	}
}

func (*teardownAdapter) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (adapter *teardownAdapter) Dispose(closeContext context.Context) error {
	if adapter.teardown != nil {
		adapter.teardown.BeginTeardown(closeContext)
	}
	return nil
}

var _ plugin.Plugin = (*teardownAdapter)(nil)
