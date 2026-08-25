package childscope

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/tools"
)

// OneShotInput contains the resolved policy and run-local Plugins needed to
// provision one one-shot child Scope.
type OneShotInput struct {
	Persona    *string
	ToolFilter *tools.ToolRestriction
	Plugins    []plugin.Plugin
}

// OneShotBuilder builds the scoped capabilities of one fresh one-shot child.
// It is separate from ContinuableBuilder because a terminal Run has no Activation
// Extensions or cold-resume lifecycle.
type OneShotBuilder struct {
	approval approval.DelegationPolicy
}

// NewOneShot constructs the one-shot child Scope builder.
func NewOneShot(approvalPolicy approval.DelegationPolicy) *OneShotBuilder {
	return &OneShotBuilder{
		approval: approvalPolicy,
	}
}

// Provisioner returns one child Provisioner containing inherited delegation
// policy, optional persona and Tool restriction, followed by run-local Plugins.
func (owner *OneShotBuilder) Provisioner(input OneShotInput) agent.Provisioner {
	if owner == nil {
		return nil
	}
	shared := childPlugins(owner.approval, true, input.Persona, input.ToolFilter)
	instances := make([]plugin.Plugin, 0, len(shared)+len(input.Plugins))
	instances = append(instances, shared...)
	instances = append(instances, input.Plugins...)
	if len(instances) == 0 {
		return nil
	}
	return scopedplugin.MountPlugins(instances...)
}
