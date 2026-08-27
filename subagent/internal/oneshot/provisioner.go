package oneshot

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childpolicy"
)

// provisioner resolves the child-scoped effects required by this exact
// OneShot command. OneShot owns the descriptor and structured-result Plugins;
// shared policy adapters remain implemented by childpolicy.
func (owner *Service) provisioner(
	settings subagent.OneShotOptions,
	descriptor subagent.OneShotDescriptor,
) (agent.Provisioner, *structuredOutput) {
	instances := childpolicy.Plugins(
		childpolicy.PolicySet{
			Delegation:      owner.dependencies.Delegation,
			Persona:         settings.Persona,
			ToolRestriction: settings.ToolFilter,
		},
	)
	instances = append(
		instances,
		&descriptorAppender{
			descriptor: descriptor,
		},
	)
	var structured *structuredOutput
	if len(settings.OutputSchema) != 0 {
		structured = newStructuredOutput(settings.OutputSchema)
		instances = append(instances, structured)
	}
	return agent.ComposeProvisioners(
		scopedplugin.MountPlugins(instances...),
		owner.dependencies.Extensions,
	), structured
}
