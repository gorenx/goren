package composition

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
)

type delegationPolicy struct {
	plugin.Base
	policy approval.DelegationPolicy
	child  agent.Agent
}

func (*delegationPolicy) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "@goren/subagent/delegation-policy",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[agent.Agent](),
		},
	}
}

func (policy *delegationPolicy) Apply(context.Context) error {
	childAgent, found := plugin.Resolve[agent.Agent](policy)
	if !found {
		return errors.New("subagent: delegated child Agent is unavailable")
	}
	policy.child = childAgent
	return policy.policy.SeedDelegationPolicy(childAgent.SessionValue())
}

func (policy *delegationPolicy) Dispose(context.Context) error {
	policy.child = nil
	return nil
}
