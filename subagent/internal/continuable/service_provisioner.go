package continuable

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childpolicy"
)

// provisioner resolves the child-scoped effects recorded by the Continuable
// descriptor. Only a fresh child receives the parent delegation policy.
func (owner *Service) provisioner(
	descriptor subagent.ContinuableDescriptor,
	fresh bool,
) agent.Provisioner {
	var delegation approval.DelegationPolicy
	if fresh {
		delegation = owner.dependencies.Delegation
	}
	instances := childpolicy.Plugins(
		childpolicy.PolicySet{
			Delegation:      delegation,
			Persona:         descriptor.Persona,
			ToolRestriction: descriptor.ToolFilter,
		},
	)
	var policies agent.Provisioner
	if len(instances) != 0 {
		policies = scopedplugin.MountPlugins(instances...)
	}
	return agent.ComposeProvisioners(
		policies,
		owner.dependencies.Extensions,
	)
}
