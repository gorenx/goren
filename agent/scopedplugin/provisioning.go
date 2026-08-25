// Package scopedplugin adapts Plugin installation to Agent Scope
// provisioning without introducing Plugin types into the agent package.
package scopedplugin

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
)

// Scope is the Plugin adapter capability implemented by Agent Loop's exact
// per-Agent Scope root.
type Scope interface {
	agent.Scope
	MountPlugin(context.Context, plugin.Plugin) (agent.ScopeResource, error)
}

// Mount installs one Plugin through a Scope that explicitly offers Plugin
// composition. Agent business Scope implementations need not support it.
func Mount(
	requestContext context.Context,
	target agent.Scope,
	instance plugin.Plugin,
) (agent.ScopeResource, error) {
	pluginScope, matches := target.(Scope)
	if !matches {
		return nil, errors.New("agent/scopedplugin: Scope does not support Plugins")
	}
	return pluginScope.MountPlugin(requestContext, instance)
}

// MountPlugins builds a Plugin-aware Agent Provisioner. Successful mounts are
// structurally owned by the Agent Scope; a partial failure releases every
// mount acquired by this call in reverse order.
func MountPlugins(instances ...plugin.Plugin) agent.Provisioner {
	return &provisioner{
		instances: append([]plugin.Plugin(nil), instances...),
	}
}

type provisioner struct {
	instances []plugin.Plugin
}

func (owner *provisioner) Provision(
	requestContext context.Context,
	target agent.Scope,
) (agent.Provisioning, error) {
	if owner == nil {
		return nil, errors.New("agent/scopedplugin: Provisioner is nil")
	}
	mounted := make([]agent.ScopeResource, 0, len(owner.instances))
	for index, instance := range owner.instances {
		if instance == nil {
			return nil, errors.Join(
				fmt.Errorf("agent/scopedplugin: Plugin is nil at index %d", index),
				disposeResources(
					context.WithoutCancel(requestContext),
					mounted,
				),
			)
		}
		resource, err := Mount(requestContext, target, instance)
		if err != nil {
			return nil, errors.Join(
				err,
				disposeResources(
					context.WithoutCancel(requestContext),
					mounted,
				),
			)
		}
		mounted = append(mounted, resource)
	}
	return nil, nil
}

func disposeResources(
	closeContext context.Context,
	resources []agent.ScopeResource,
) error {
	var closeErr error
	for index := len(resources) - 1; index >= 0; index-- {
		closeErr = errors.Join(
			closeErr,
			resources[index].Dispose(closeContext),
		)
	}
	return closeErr
}

var _ agent.Provisioner = (*provisioner)(nil)
