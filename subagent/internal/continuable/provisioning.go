package continuable

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childpolicy"
	extensionregistry "github.com/gorenx/goren/subagent/internal/extension"
)

type scopePolicy struct {
	descriptor subagent.ContinuableDescriptor
	delegation approval.DelegationPolicy
}

func (owner *Service) provisioner(policy scopePolicy) agent.Provisioner {
	instances := childpolicy.Plugins(
		childpolicy.PolicySet{
			Delegation:      policy.delegation,
			Persona:         policy.descriptor.Persona,
			ToolRestriction: policy.descriptor.ToolFilter,
		},
	)
	var policies agent.Provisioner
	if len(instances) != 0 {
		policies = scopedplugin.MountPlugins(instances...)
	}
	var extensions agent.Provisioner
	if owner.dependencies.Extensions != nil {
		extensions = extensionregistry.NewProvisioner(owner.dependencies.Extensions)
	}
	if policies == nil && extensions == nil {
		return nil
	}
	return &scopeProvisioner{
		policies:   policies,
		extensions: extensions,
	}
}

// scopeProvisioner applies child policy before installing Continuable-only
// Extensions. Policy resources transfer directly to the Agent Scope; the
// Extension Provisioning remains the one publication transaction returned.
type scopeProvisioner struct {
	policies   agent.Provisioner
	extensions agent.Provisioner
}

func (owner *scopeProvisioner) Provision(
	requestContext context.Context,
	target agent.Scope,
) (agent.Provisioning, error) {
	if owner.policies != nil {
		if _, err := owner.policies.Provision(requestContext, target); err != nil {
			return nil, err
		}
	}
	if owner.extensions == nil {
		return nil, nil
	}
	return owner.extensions.Provision(requestContext, target)
}

var _ agent.Provisioner = (*scopeProvisioner)(nil)
