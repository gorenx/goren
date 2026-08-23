// Package composition builds the Agent Provisioner for one continuable
// child without owning continuation admission or residency.
package composition

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent/internal/continuation"
	activationextension "github.com/gorenx/goren/subagent/internal/extension"
	"github.com/gorenx/goren/tools"
)

// ContinuableComposer owns the deployment capabilities installed in one
// continuable child Scope.
type ContinuableComposer struct {
	approval   approval.DelegationPolicy
	extensions *activationextension.Registry
}

// NewContinuable constructs a continuable child Composer.
func NewContinuable(
	approvalPolicy approval.DelegationPolicy,
	extensionRegistry *activationextension.Registry,
) *ContinuableComposer {
	return &ContinuableComposer{
		approval:   approvalPolicy,
		extensions: extensionRegistry,
	}
}

// Compose builds a fresh Provisioner for one materialization. Delegation
// approval is seeded only for fresh Sessions; cold resume replays the durable
// policy.
func (owner *ContinuableComposer) Compose(
	input continuation.Composition,
) agent.Provisioner {
	if owner == nil {
		return nil
	}
	parts := make([]agent.Provisioner, 0, 2)
	instances := owner.buildPlugins(input)
	if len(instances) != 0 {
		parts = append(
			parts,
			agent.MountPlugins(instances...),
		)
	}
	if owner.extensions != nil {
		parts = append(
			parts,
			activationextension.NewProvisioner(
				owner.extensions,
				activationextension.Input{
					ChildID:    input.ChildID,
					ParentID:   input.ParentID,
					Descriptor: input.Descriptor,
				},
			),
		)
	}
	if len(parts) == 0 {
		return nil
	}
	return &provisioner{
		parts: parts,
	}
}

func (owner *ContinuableComposer) buildPlugins(
	input continuation.Composition,
) []plugin.Plugin {
	return childPlugins(
		owner.approval,
		input.Fresh,
		input.Descriptor.Persona,
		input.Descriptor.ToolFilter,
	)
}

func childPlugins(
	approvalPolicy approval.DelegationPolicy,
	fresh bool,
	personaText *string,
	toolFilter *tools.ToolRestriction,
) []plugin.Plugin {
	instances := make([]plugin.Plugin, 0, 3)
	if approvalPolicy != nil && fresh {
		instances = append(
			instances,
			&delegationPolicy{
				policy: approvalPolicy,
			},
		)
	}
	if personaText != nil {
		instances = append(
			instances,
			newPersona(*personaText),
		)
	}
	if toolFilter != nil {
		instances = append(
			instances,
			newToolRestriction(*toolFilter),
		)
	}
	return instances
}

var _ continuation.Composer = (*ContinuableComposer)(nil)
