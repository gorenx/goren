package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/plugin"
)

// Effect is one exact idempotent resource owned by an unpublished Agent Scope.
// An effect may be disposed early; later Scope teardown must remain safe.
type Effect interface {
	Dispose(context.Context) error
}

// Scope is the composition boundary of one active but unpublished Agent.
// Setup code may mount scoped Plugins or transfer ordinary effects to it, but
// must not drive the Agent before Registry.Create or Registry.Resume returns.
type Scope interface {
	AgentValue() Agent
	Mount(context.Context, plugin.Plugin) (Effect, error)
	Own(Effect) error
}

// Setup owns one exact unpublished Agent composition lifecycle. Callers must
// provide a fresh instance for each Create or Resume operation.
type Setup interface {
	Prepare(context.Context, Scope) error
	Commit() error
	Dispose(context.Context) error
}

// MountPlugins builds a Setup that mounts every Plugin in declaration order.
// The Agent Scope owns rollback and reverse teardown of successful mounts.
func MountPlugins(instances ...plugin.Plugin) Setup {
	return &pluginSetup{
		instances: append([]plugin.Plugin(nil), instances...),
	}
}

type pluginSetup struct {
	instances []plugin.Plugin
}

func (composition *pluginSetup) Prepare(
	requestContext context.Context,
	target Scope,
) error {
	if composition == nil {
		return errors.New("agent: Plugin Setup is nil")
	}
	for index, instance := range composition.instances {
		if instance == nil {
			return fmt.Errorf(
				"agent: mounted Setup Plugin is nil at index %d",
				index,
			)
		}
		if _, err := target.Mount(requestContext, instance); err != nil {
			return err
		}
	}
	return nil
}

func (*pluginSetup) Commit() error { return nil }

func (*pluginSetup) Dispose(context.Context) error { return nil }
