package agentloop

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// preparedAgent owns one active but unpublished Agent creation transaction.
// Ownership transfers to the returned Handle only after membership publication.
type preparedAgent struct {
	owner     *Plugin
	subject   *ReactLoopAgent
	tree      *agentTree
	lifecycle *agentLifecycle
}

func newPreparedAgent(
	requestContext context.Context,
	owner *Plugin,
	conversation *session.Session,
	loopOptions agent.Options,
) (*preparedAgent, error) {
	if owner == nil {
		return nil, errors.New("agentloop: Agent Loop Plugin is nil")
	}
	if conversation == nil {
		return nil, errors.New("agentloop: prepared Session is nil")
	}
	subject, err := newReactLoopAgent(
		conversation,
		loopOptions,
		owner.maxParallelToolCalls,
		owner.failures,
	)
	if err != nil {
		return nil, err
	}
	tree := newAgentTree(subject, loopOptions)
	rootHandle, err := plugin.MountScopedChild(requestContext, owner, tree)
	if err != nil {
		return nil, err
	}
	lifecycle := newAgentLifecycle(owner, rootHandle)
	return &preparedAgent{
		owner:     owner,
		subject:   subject,
		tree:      tree,
		lifecycle: lifecycle,
	}, nil
}

func (prepared *preparedAgent) publish(
	requestContext context.Context,
	scopeProvisioner agent.Provisioner,
	startSource agent.SessionStartSource,
) (handle agent.Handle, publishErr error) {
	defer func() {
		if publishErr != nil {
			publishErr = errors.Join(
				publishErr,
				prepared.lifecycle.Dispose(
					context.WithoutCancel(requestContext),
				),
			)
		}
	}()
	if scopeProvisioner != nil {
		acquired, err := scopeProvisioner.Provision(
			requestContext,
			prepared.tree,
		)
		if err != nil {
			return agent.Handle{}, err
		}
		if acquired != nil {
			if err = prepared.tree.Own(acquired); err != nil {
				return agent.Handle{}, errors.Join(
					err,
					acquired.Dispose(context.WithoutCancel(requestContext)),
				)
			}
			if err = acquired.Commit(); err != nil {
				return agent.Handle{}, err
			}
		}
	}
	initiator, _ := agent.InitiatorFrom(requestContext)
	membership := newAgentMembership(
		prepared.owner.runtimeContextEvents,
		prepared.owner.failures,
		prepared.subject,
		startSource,
		initiator,
	)
	if _, err := plugin.MountChild(requestContext, prepared.tree, membership); err != nil {
		return agent.Handle{}, err
	}
	return agent.NewHandle(prepared.subject, prepared.lifecycle)
}
