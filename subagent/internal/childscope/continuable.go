// Package childscope builds the capabilities installed into one unpublished
// Subagent child Scope without owning Agent creation or child lifecycle.
package childscope

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	ext "github.com/gorenx/goren/subagent/internal/extension"
	"github.com/gorenx/goren/tools"
)

// ContinuableInput contains the resolved facts needed to provision one
// continuable child Scope.
type ContinuableInput struct {
	ChildID    session.SessionID
	ParentID   session.SessionID
	Descriptor subagent.ContinuableDescriptor
	Fresh      bool
}

// ContinuableBuilder owns the deployment capabilities installed in one
// continuable child Scope.
type ContinuableBuilder struct {
	approval   approval.DelegationPolicy
	extensions *ext.Registry
}

// NewContinuable constructs a continuable child Scope builder.
func NewContinuable(
	approvalPolicy approval.DelegationPolicy,
	extensionRegistry *ext.Registry,
) *ContinuableBuilder {
	return &ContinuableBuilder{
		approval:   approvalPolicy,
		extensions: extensionRegistry,
	}
}

// Provisioner builds one Provisioner for a materialization. Delegation
// approval is seeded only for fresh Sessions; cold resume replays the durable
// policy.
func (owner *ContinuableBuilder) Provisioner(
	input ContinuableInput,
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
			ext.NewProvisioner(
				owner.extensions,
				ext.Input{
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

func (owner *ContinuableBuilder) buildPlugins(
	input ContinuableInput,
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
