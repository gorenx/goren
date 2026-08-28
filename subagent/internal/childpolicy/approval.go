package childpolicy

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
)

// delegationSeed writes the durable approval override while the delegated
// Agent is still unpublished. It owns no resident Scope resource.
type delegationSeed struct {
	policy approval.DelegationPolicy
}

// DelegationSeed returns the one-time Provisioner for a fresh delegated Agent.
func DelegationSeed(policy approval.DelegationPolicy) agent.Provisioner {
	if policy == nil {
		return nil
	}
	return &delegationSeed{
		policy: policy,
	}
}

func (seed *delegationSeed) Provision(
	requestContext context.Context,
	target agent.Scope,
) (agent.Provisioning, error) {
	if seed == nil || seed.policy == nil || target == nil {
		return nil, errors.New("subagent: delegated Agent is unavailable")
	}
	childSubject := target.Agent()
	if childSubject == nil || childSubject.SessionValue() == nil {
		return nil, errors.New("subagent: delegated Agent is unavailable")
	}
	return nil, seed.policy.SeedDelegationPolicy(
		requestContext,
		childSubject.SessionValue(),
	)
}

var _ agent.Provisioner = (*delegationSeed)(nil)
