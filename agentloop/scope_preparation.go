package agentloop

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/session"
)

// agentScopeFactory is the Agent construction boundary for preparing one
// private runtime Scope. The Plugin adapter is the only implementation.
type agentScopeFactory interface {
	Prepare(agent.Options, session.Header) scopePreparation
}

// scopePreparation owns one unpublished Agent Scope construction transaction.
// The Agent receives its runtime port before the Scope Plugin is mounted; Mount
// then binds that exact Agent and activates the private Scope atomically.
type scopePreparation interface {
	Runtime() agent.AgentScopeRuntime
	BindTeardown(agent.AgentTeardown) error
	Mount(context.Context, *ReactLoopAgent) (scopedplugin.Scope, error)
	Rollback(context.Context) error
}

type agentScopePreparation struct {
	scopes  *ScopeSet
	root    *AgentScope
	scopeID scopeID
}

func (preparation *agentScopePreparation) Runtime() agent.AgentScopeRuntime {
	return preparation.root
}

func (preparation *agentScopePreparation) BindTeardown(
	teardownTarget agent.AgentTeardown,
) error {
	return preparation.root.bindTeardown(teardownTarget)
}

func (preparation *agentScopePreparation) Mount(
	requestContext context.Context,
	subject *ReactLoopAgent,
) (scopedplugin.Scope, error) {
	if preparation == nil || preparation.root == nil || preparation.scopes == nil {
		return nil, errors.New("agentloop: Agent Scope preparation is unavailable")
	}
	if err := preparation.root.bind(subject); err != nil {
		return nil, err
	}
	return preparation.scopes.mount(
		requestContext,
		preparation.scopeID,
		preparation.root,
	)
}

func (preparation *agentScopePreparation) Rollback(
	closeContext context.Context,
) error {
	if preparation == nil || preparation.root == nil {
		return nil
	}
	return preparation.root.Teardown(closeContext)
}
