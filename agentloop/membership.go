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

// agentMembership owns externally visible Agent and Session membership. It is
// mounted only after Provisioning commits, so neither Registry can observe a partial
// Agent Scope.
type agentMembership struct {
	plugin.Base
	router      *runtimeContextRouter
	failures    observerFailureReporter
	subject     *ReactLoopAgent
	startSource agent.SessionStartSource
	initiator   agent.Agent
	lifecycle   *agentLifecycle

	mutex         sync.Mutex
	agents        agent.Registry
	sessionHandle session.Handle
	routed        bool
	registered    bool
	published     bool
	closing       bool
	closed        chan struct{}
	closeErr      error
}

func newAgentMembership(
	router *runtimeContextRouter,
	failures observerFailureReporter,
	subject *ReactLoopAgent,
	lifecycle *agentLifecycle,
	startSource agent.SessionStartSource,
	initiator agent.Agent,
) *agentMembership {
	return &agentMembership{
		router:      router,
		failures:    failures,
		subject:     subject,
		lifecycle:   lifecycle,
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

func (membership *agentMembership) Apply(
	requestContext context.Context,
) error {
	agents, err := plugin.Require[agent.Registry](membership)
	if err != nil {
		return err
	}
	sessions, err := plugin.Require[session.LiveStore](membership)
	if err != nil {
		return err
	}
	membership.mutex.Lock()
	membership.agents = agents
	membership.mutex.Unlock()

	if err = membership.router.register(
		membership.subject.conversation,
		membership.subject.loop.runtimeContextView(),
	); err != nil {
		return err
	}
	membership.mutex.Lock()
	membership.routed = true
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
	if err = membership.subject.loop.beginServing(); err != nil {
		return err
	}
	if err = sessions.Announce(
		requestContext,
		membership.subject.conversation,
	); err != nil {
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
		membership.failures.report(fmt.Errorf(
			"agentloop: Agent %q session-start observer: %w",
			membership.subject.ID(),
			observerErr,
		))
	}
	if err := requestContext.Err(); err != nil {
		return err
	}
	membership.mutex.Lock()
	membership.published = true
	membership.mutex.Unlock()
	return nil
}

func (membership *agentMembership) Dispose(
	closeContext context.Context,
) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	membership.mutex.Lock()
	published := membership.published
	if published && membership.lifecycle != nil {
		membership.lifecycle.beginClosing()
	}
	if membership.closing {
		closed := membership.closed
		membership.mutex.Unlock()
		select {
		case <-closed:
			membership.mutex.Lock()
			closeErr := membership.closeErr
			membership.mutex.Unlock()
			return closeErr
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	}
	membership.closing = true
	agents := membership.agents
	sessionHandle := membership.sessionHandle
	routed := membership.routed
	registered := membership.registered
	membership.mutex.Unlock()

	// Already-started Tool bodies may hold dependencies from the Agent Tree.
	// Their drain is structural teardown and cannot be abandoned on caller
	// cancellation without making the remaining Runtime shutdown unsafe.
	drainContext := context.WithoutCancel(closeContext)
	closeErr := membership.subject.loop.quiesce(drainContext)
	if registered && agents != nil {
		closeErr = errors.Join(
			closeErr,
			agents.Remove(drainContext, membership.subject),
		)
	}
	if routed {
		membership.router.remove(
			membership.subject.conversation,
			membership.subject.loop.runtimeContextView(),
		)
	}
	if sessionHandle != nil {
		closeErr = errors.Join(
			closeErr,
			sessionHandle.Release(drainContext),
		)
	}
	membership.mutex.Lock()
	membership.closeErr = closeErr
	close(membership.closed)
	membership.mutex.Unlock()
	return closeErr
}
