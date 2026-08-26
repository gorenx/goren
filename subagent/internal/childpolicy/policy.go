// Package childpolicy adapts durable Subagent policy into child-scoped
// Plugins shared by OneShot and Continuable implementations.
package childpolicy

import (
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/tools"
)

// PolicySet describes the child-local policy effects required by one Agent.
type PolicySet struct {
	Delegation      approval.DelegationPolicy
	Persona         *string
	ToolRestriction *tools.ToolRestriction
}

// Plugins builds the child-scoped policy adapters in deterministic order.
func Plugins(selected PolicySet) []plugin.Plugin {
	instances := make([]plugin.Plugin, 0, 3)
	if selected.Delegation != nil {
		instances = append(
			instances,
			&delegationPolicy{
				policy: selected.Delegation,
			},
		)
	}
	if selected.Persona != nil {
		instances = append(instances, newPersona(*selected.Persona))
	}
	if selected.ToolRestriction != nil {
		instances = append(
			instances,
			newToolRestriction(*selected.ToolRestriction),
		)
	}
	return instances
}
