package agentloop

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop/internal/visiblecontext"
	"github.com/gorenx/goren/session"
)

// preparedAgent owns one unpublished Agent construction transaction. The
// Registry AgentEpoch owns the lifecycle before the Agent is published.
type preparedAgent struct {
	subject         *ReactLoopAgent
	preparation     scopePreparation
	visibleContexts *visiblecontext.Directory
}

func newPreparedAgent(
	conversation session.Context,
	agentOptions agent.Options,
	maxParallelToolCalls int,
	reportObserverError func(error),
	visibleContexts *visiblecontext.Directory,
	scopes agentScopeFactory,
) (*preparedAgent, error) {
	if conversation == nil {
		return nil, errors.New("agentloop: prepared Session is nil")
	}
	if scopes == nil {
		return nil, errors.New("agentloop: Agent Scope factory is unavailable")
	}
	preparation := scopes.Prepare(agentOptions, conversation.Header())
	if preparation == nil || preparation.Runtime() == nil {
		return nil, errors.New("agentloop: Agent Scope preparation is invalid")
	}
	subject, err := newReactLoopAgent(
		conversation,
		agentOptions,
		maxParallelToolCalls,
		reportObserverError,
		preparation.Runtime(),
	)
	if err != nil {
		return nil, err
	}
	return &preparedAgent{
		subject:         subject,
		preparation:     preparation,
		visibleContexts: visibleContexts,
	}, nil
}

func (prepared *preparedAgent) publish(
	requestContext context.Context,
	agentEpoch agent.AgentEpoch,
	scopeProvisioner agent.Provisioner,
) (publishErr error) {
	defer func() {
		if publishErr != nil {
			publishErr = errors.Join(
				publishErr,
				prepared.preparation.Rollback(
					context.WithoutCancel(requestContext),
				),
			)
		}
	}()
	scope, err := prepared.preparation.Mount(
		requestContext,
		prepared.subject,
	)
	if err != nil {
		return err
	}
	if scopeProvisioner != nil {
		if err = agent.ApplyProvisioning(
			requestContext,
			scope,
			scopeProvisioner,
		); err != nil {
			return err
		}
	}
	binding := newSessionBinding(
		prepared.visibleContexts,
		prepared.subject,
	)
	if _, err = scope.MountPlugin(requestContext, binding); err != nil {
		return err
	}
	epochTeardown, err := agentEpoch.Attach(
		prepared.subject,
		prepared.subject.scopeRuntime,
	)
	if err != nil {
		return err
	}
	if err = prepared.preparation.BindTeardown(epochTeardown); err != nil {
		return err
	}
	if err = binding.announce(requestContext); err != nil {
		return err
	}
	_, err = scope.MountPlugin(
		requestContext,
		&teardownAdapter{
			teardown: epochTeardown,
		},
	)
	return err
}
