// Package composition builds the Agent Provisioner for one continuable
// child without owning continuation admission or residency.
package composition

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent/internal/continuation"
	activationextension "github.com/gorenx/goren/subagent/internal/extension"
)

// Composer owns the deployment capabilities installed in a child Scope.
type Composer struct {
	approval   approval.DelegationPolicy
	extensions *activationextension.Registry
}

// New constructs a child Composer from optional owner-defined capabilities.
func New(
	approvalPolicy approval.DelegationPolicy,
	extensionRegistry *activationextension.Registry,
) *Composer {
	return &Composer{
		approval:   approvalPolicy,
		extensions: extensionRegistry,
	}
}

// Compose builds a fresh Provisioner for one materialization. Delegation
// approval is seeded only for fresh Sessions; cold resume replays the durable
// policy.
func (owner *Composer) Compose(
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

func (owner *Composer) buildPlugins(
	input continuation.Composition,
) []plugin.Plugin {
	instances := make([]plugin.Plugin, 0, 3)
	if owner.approval != nil && input.Fresh {
		instances = append(
			instances,
			&delegationPolicy{
				policy: owner.approval,
			},
		)
	}
	if input.Descriptor.Persona != nil {
		instances = append(
			instances,
			newPersona(*input.Descriptor.Persona),
		)
	}
	if input.Descriptor.ToolFilter != nil {
		instances = append(
			instances,
			newToolRestriction(*input.Descriptor.ToolFilter),
		)
	}
	return instances
}

var _ continuation.Composer = (*Composer)(nil)
