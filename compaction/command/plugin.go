package command

import (
	"context"

	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/plugin"
)

// PluginName is the canonical Harness /compact Consumer Plugin name.
const PluginName = "@deepseek-ai/dsh-command-compact"

// Plugin owns /compact registration, admitted invocation drainage, and
// dependency lifetime for one Compact business operation.
type Plugin struct {
	plugin.Base
	implementation Compact
	handle         *commands.Registration
}

// New constructs an inactive /compact Consumer.
func New() *Plugin {
	return &Plugin{}
}

// Manifest declares the Commands and Compaction capabilities consumed by this
// plugin.
func (owner *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[commands.Registry](),
			plugin.ServiceOf[compaction.Engine](),
		},
	}
}

// Apply binds the backend-independent operation and registers /compact.
func (owner *Plugin) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	commandRegistry, err := plugin.Require[commands.Registry](owner)
	if err != nil {
		return err
	}
	compactionEngine, err := plugin.Require[compaction.Engine](owner)
	if err != nil {
		return err
	}
	owner.implementation.bind(compactionEngine)
	handle, err := commandRegistry.Register(commands.Definition{
		Name:        "compact",
		Description: "Compact older conversation history",
		Handler:     owner.implementation.Execute,
	})
	if err != nil {
		owner.implementation.release()
		return err
	}
	owner.handle = handle
	return nil
}

// Dispose first withdraws /compact, then waits for every already-admitted
// handler to finish its Compaction close and flush boundaries.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	if owner.handle == nil {
		owner.implementation.release()
		return nil
	}
	owner.handle.Unregister()
	waitErr := owner.handle.Wait(closeContext)
	if waitErr == nil {
		owner.handle = nil
		owner.implementation.release()
	}
	return waitErr
}
