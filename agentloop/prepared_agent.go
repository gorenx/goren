package agentloop

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

// preparedAgent owns one unpublished Agent construction transaction. The
// Registry AgentEpoch owns the lifecycle before the Agent is published.
type preparedAgent struct {
	subject     *ReactLoopAgent
	preparation scopePreparation
	router      *runtimeContextRouter
}

func newPreparedAgent(
	conversation session.Context,
	loopOptions agent.Options,
	maxParallelToolCalls int,
	failures observerFailureReporter,
	router *runtimeContextRouter,
	scopes scopeHost,
) (*preparedAgent, error) {
	if conversation == nil {
		return nil, errors.New("agentloop: prepared Session is nil")
	}
	if scopes == nil {
		return nil, errors.New("agentloop: Agent Scope host is unavailable")
	}
	preparation := scopes.Prepare(loopOptions, conversation.Header())
	if preparation == nil || preparation.Runtime() == nil {
		return nil, errors.New("agentloop: Agent Scope preparation is invalid")
	}
	subject, err := newReactLoopAgent(
		conversation,
		loopOptions,
		maxParallelToolCalls,
		failures,
		preparation.Runtime(),
	)
	if err != nil {
		return nil, err
	}
	return &preparedAgent{
		subject:     subject,
		preparation: preparation,
		router:      router,
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
		prepared.router,
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
