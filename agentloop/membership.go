package agentloop

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// agentMembership owns the externally visible Agent and Session membership.
// Its commit activation guarantees the ordinary Agent Plugin tree is ready
// before either Registry announces it.
type agentMembership struct {
	plugin.Base
	owner       *LoopPlugin
	lifecycle   *agentLifecycle
	subject     *ReactLoopAgent
	startSource agent.SessionStartSource
	initiator   agent.Agent

	mutex         sync.Mutex
	agents        agent.Registry
	sessionHandle session.SessionHandle
	tracked       bool
	registered    bool
	closing       bool
	closed        chan struct{}
	closeErr      error
}

func newAgentMembership(
	owner *LoopPlugin,
	lifecycle *agentLifecycle,
	subject *ReactLoopAgent,
	startSource agent.SessionStartSource,
	initiator agent.Agent,
) *agentMembership {
	return &agentMembership{
		owner:       owner,
		lifecycle:   lifecycle,
		subject:     subject,
		startSource: startSource,
		initiator:   initiator,
		closed:      make(chan struct{}),
	}
}

func (*agentMembership) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName + "/agent-membership",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[agent.Registry](),
			plugin.ServiceOf[session.LiveStore](),
		},
	}
}

func (membership *agentMembership) Apply(requestContext context.Context) error {
	agents, err := plugin.Require[agent.Registry](membership)
	if err != nil {
		return err
	}
	sessions, err := plugin.Require[session.LiveStore](membership)
	if err != nil {
		return err
	}
	if err = membership.owner.track(membership.lifecycle); err != nil {
		return err
	}
	membership.mutex.Lock()
	membership.agents = agents
	membership.tracked = true
	membership.mutex.Unlock()

	sessionHandle, err := sessions.Enter(membership.subject.conversation)
	if err != nil {
		return err
	}
	membership.mutex.Lock()
	membership.sessionHandle = sessionHandle
	membership.mutex.Unlock()
	if err = agents.Enter(membership.subject, membership.initiator); err != nil {
		return err
	}
	membership.mutex.Lock()
	membership.registered = true
	membership.mutex.Unlock()
	if err = sessions.Announce(requestContext, membership.subject.conversation); err != nil {
		return err
	}
	if err = agents.Announce(requestContext, membership.subject); err != nil {
		return err
	}
	if observerErr := plugin.Publish(
		requestContext,
		membership.subject,
		agent.SessionStarted{
			Subject: membership.subject,
			Source:  membership.startSource,
		},
	); observerErr != nil {
		membership.owner.report(fmt.Errorf(
			"agentloop: Agent %q session-start observer: %w",
			membership.subject.ID(),
			observerErr,
		))
	}
	return requestContext.Err()
}

func (membership *agentMembership) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	membership.mutex.Lock()
	if membership.closing {
		closed := membership.closed
		membership.mutex.Unlock()
		<-closed
		membership.mutex.Lock()
		closeErr := membership.closeErr
		membership.mutex.Unlock()
		return closeErr
	}
	membership.closing = true
	agents := membership.agents
	sessionHandle := membership.sessionHandle
	tracked := membership.tracked
	registered := membership.registered
	membership.mutex.Unlock()

	membership.subject.beginDispose()
	closeErr := membership.subject.WhenIdle(context.Background())
	if registered && agents != nil {
		closeErr = errors.Join(
			closeErr,
			agents.Remove(closeContext, membership.subject),
		)
	}
	if sessionHandle != nil {
		closeErr = errors.Join(
			closeErr,
			sessionHandle.Release(closeContext),
		)
	}
	if tracked {
		membership.owner.forget(membership.lifecycle)
	}
	membership.mutex.Lock()
	membership.closeErr = closeErr
	close(membership.closed)
	membership.mutex.Unlock()
	return closeErr
}
