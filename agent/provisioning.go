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
// Provisioners may mount scoped Plugins or transfer ordinary effects to it,
// but must not drive the Agent before Registry.Create or Registry.Resume
// returns.
type Scope interface {
	Agent() Agent
	Mount(context.Context, plugin.Plugin) (Effect, error)
	Own(Effect) error
}

// Provisioner configures one unpublished Agent Scope. It returns nil when all
// acquired resources have already transferred to the Scope and no publication
// validation remains. A failed call must release every resource it has not
// transferred to the Scope.
type Provisioner interface {
	Provision(context.Context, Scope) (Provisioning, error)
}

// Provisioning owns resources that survive successful provisioning. Commit
// validates the exact publication boundary; Dispose releases the resources
// when creation rolls back or the Agent leaves residency.
type Provisioning interface {
	Effect
	Commit() error
}

// MountPlugins returns a Provisioner that mounts every Plugin in declaration
// order. The Agent tree structurally owns successful mounts, so provisioning
// produces no additional lifecycle object.
func MountPlugins(instances ...plugin.Plugin) Provisioner {
	return &pluginProvisioner{
		instances: append([]plugin.Plugin(nil), instances...),
	}
}

type pluginProvisioner struct {
	instances []plugin.Plugin
}

func (owner *pluginProvisioner) Provision(
	requestContext context.Context,
	target Scope,
) (Provisioning, error) {
	if owner == nil {
		return nil, errors.New("agent: Plugin Provisioner is nil")
	}
	for index, instance := range owner.instances {
		if instance == nil {
			return nil, fmt.Errorf(
				"agent: mounted Provisioner Plugin is nil at index %d",
				index,
			)
		}
		if _, err := target.Mount(requestContext, instance); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

var _ Provisioner = (*pluginProvisioner)(nil)
