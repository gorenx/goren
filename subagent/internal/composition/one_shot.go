package composition

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/tools"
)

// OneShotComposer builds the scoped capabilities of one fresh one-shot child.
// It is separate from ContinuableComposer because a terminal Run has no Activation
// Extensions or cold-resume lifecycle.
type OneShotComposer struct {
	approval approval.DelegationPolicy
}

// NewOneShot constructs the one-shot child composer.
func NewOneShot(approvalPolicy approval.DelegationPolicy) *OneShotComposer {
	return &OneShotComposer{
		approval: approvalPolicy,
	}
}

// Provisioner returns one child Provisioner containing inherited delegation
// policy, optional persona and Tool restriction, followed by run-local Plugins.
func (owner *OneShotComposer) Provisioner(
	personaText *string,
	toolFilter *tools.ToolRestriction,
	runPlugins ...plugin.Plugin,
) agent.Provisioner {
	if owner == nil {
		return nil
	}
	shared := childPlugins(owner.approval, true, personaText, toolFilter)
	instances := make([]plugin.Plugin, 0, len(shared)+len(runPlugins))
	instances = append(instances, shared...)
	instances = append(instances, runPlugins...)
	if len(instances) == 0 {
		return nil
	}
	return agent.MountPlugins(instances...)
}
