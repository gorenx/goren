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

// DelegationSeed returns the one-time Setup for a fresh delegated Agent.
func DelegationSeed(policy approval.DelegationPolicy) agent.Setup {
	if policy == nil {
		return nil
	}
	return &delegationSeed{
		policy: policy,
	}
}

func (seed *delegationSeed) Apply(
	requestContext context.Context,
	childSubject agent.Agent,
	_ agent.ScopeEditor,
) error {
	if seed == nil || seed.policy == nil {
		return errors.New("subagent: delegated Agent is unavailable")
	}
	if childSubject == nil || childSubject.SessionValue() == nil {
		return errors.New("subagent: delegated Agent is unavailable")
	}
	return seed.policy.SeedDelegationPolicy(
		requestContext,
		childSubject.SessionValue(),
	)
}

var _ agent.Setup = (*delegationSeed)(nil)
