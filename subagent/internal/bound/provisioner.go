package bound

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childpolicy"
	"github.com/gorenx/goren/tools"
)

// provisioner composes the Agent Provisioner used by one new Bound epoch from
// the exact parent projection snapshot. Plugin wiring supplies only Extension
// resolution; it does not interpret Bound configuration.
func (owner *Service) provisioner(
	config subagent.BoundConfigSnapshot,
	fresh bool,
) (agent.Provisioner, error) {
	if owner == nil || owner.dependencies.Extensions == nil {
		return nil, errors.New(
			"subagent: Bound Extension selection is unavailable",
		)
	}
	detached := cloneBoundConfig(config)
	selectedExtensions, err := owner.dependencies.Extensions.Provision(
		detached.Extensions,
	)
	if err != nil {
		return nil, err
	}
	var delegation approval.DelegationPolicy
	if fresh {
		delegation = owner.dependencies.Delegation
	}
	policyPlugins := childpolicy.Plugins(
		childpolicy.PolicySet{
			Delegation:      delegation,
			Persona:         detached.Persona,
			ToolRestriction: detached.ToolRestriction,
		},
	)
	var policies agent.Provisioner
	if len(policyPlugins) != 0 {
		policies = scopedplugin.MountPlugins(policyPlugins...)
	}
	return agent.ComposeProvisioners(
		policies,
		owner.dependencies.CommonExtensions,
		selectedExtensions,
	), nil
}

func cloneBoundConfig(
	source subagent.BoundConfigSnapshot,
) subagent.BoundConfigSnapshot {
	detached := subagent.BoundConfigSnapshot{
		Enabled:    source.Enabled,
		Extensions: append([]string(nil), source.Extensions...),
	}
	if source.Persona != nil {
		persona := *source.Persona
		detached.Persona = &persona
	}
	if source.ToolRestriction != nil {
		detached.ToolRestriction = &tools.ToolRestriction{
			Allow: append([]string(nil), source.ToolRestriction.Allow...),
			Deny:  append([]string(nil), source.ToolRestriction.Deny...),
		}
	}
	return detached
}
