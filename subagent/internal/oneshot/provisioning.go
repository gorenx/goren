package oneshot

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent/internal/childpolicy"
	"github.com/gorenx/goren/tools"
)

type scopePolicy struct {
	persona     *string
	restriction *tools.ToolRestriction
	plugins     []plugin.Plugin
}

func (owner *Service) provisioner(policy scopePolicy) agent.Provisioner {
	instances := childpolicy.Plugins(
		childpolicy.PolicySet{
			Delegation:      owner.dependencies.Approval,
			Persona:         policy.persona,
			ToolRestriction: policy.restriction,
		},
	)
	instances = append(instances, policy.plugins...)
	if len(instances) == 0 {
		return nil
	}
	return scopedplugin.MountPlugins(instances...)
}
