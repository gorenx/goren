package agentloop

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

type sessionPublicationPhase uint8

const (
	sessionUnpublished sessionPublicationPhase = iota
	sessionPublishing
	sessionPublished
)

// sessionBinding attaches one Agent Loop to its live Session and runtime
// context projection. Agent membership and Agent lifecycle belong exclusively
// to agent.RegistryService.
type sessionBinding struct {
	plugin.Base
	router  *runtimeContextRouter
	subject *ReactLoopAgent

	mutex             sync.Mutex
	sessions          session.LiveStore
	sessionHandle     session.Handle
	routeRegistration *runtimeContextRegistration
	publication       sessionPublicationPhase
	closeOnce         sync.Once
	closed            chan struct{}
	closeErr          error
}

func newSessionBinding(
	router *runtimeContextRouter,
	subject *ReactLoopAgent,
) *sessionBinding {
	return &sessionBinding{
		router:  router,
		subject: subject,
		closed:  make(chan struct{}),
	}
}

func (*sessionBinding) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName + "/session-binding",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[session.LiveStore](),
		},
	}
}

func (binding *sessionBinding) Apply(requestContext context.Context) error {
	sessions, err := plugin.Require[session.LiveStore](binding)
	if err != nil {
		return err
	}
	routeRegistration, err := binding.router.register(
		binding.subject.conversation,
		binding.subject.loop.runtimeContextView(),
	)
	if err != nil {
		return err
	}
	sessionHandle, err := sessions.Enter(binding.subject.conversation)
	if err != nil {
		routeRegistration.Release()
		return err
	}
	binding.mutex.Lock()
	binding.sessions = sessions
	binding.sessionHandle = sessionHandle
	binding.routeRegistration = routeRegistration
	binding.mutex.Unlock()
	if err = binding.subject.loop.beginServing(); err != nil {
		return errors.Join(
			err,
			binding.Dispose(context.WithoutCancel(requestContext)),
		)
	}
	return requestContext.Err()
}

func (binding *sessionBinding) announce(
	requestContext context.Context,
) error {
	binding.mutex.Lock()
	if binding.publication != sessionUnpublished {
		binding.mutex.Unlock()
		return errors.New("agentloop: Session binding was already announced")
	}
	binding.publication = sessionPublishing
	sessions := binding.sessions
	binding.mutex.Unlock()
	if sessions == nil {
		binding.mutex.Lock()
		binding.publication = sessionUnpublished
		binding.mutex.Unlock()
		return errors.New("agentloop: Session binding is not active")
	}
	err := sessions.Announce(requestContext, binding.subject.conversation)
	binding.mutex.Lock()
	if err != nil {
		binding.publication = sessionUnpublished
	} else {
		binding.publication = sessionPublished
	}
	binding.mutex.Unlock()
	return err
}

func (binding *sessionBinding) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	binding.closeOnce.Do(func() {
		binding.closeErr = binding.release(context.WithoutCancel(closeContext))
		close(binding.closed)
	})
	select {
	case <-binding.closed:
		return binding.closeErr
	case <-closeContext.Done():
		return context.Cause(closeContext)
	}
}

func (binding *sessionBinding) release(closeContext context.Context) error {
	binding.mutex.Lock()
	sessionHandle := binding.sessionHandle
	routeRegistration := binding.routeRegistration
	binding.sessionHandle = nil
	binding.routeRegistration = nil
	binding.mutex.Unlock()

	// Already-started Tool bodies may hold dependencies from the Agent Scope.
	// Their drain is structural teardown and cannot be abandoned on caller
	// cancellation without making the remaining Runtime shutdown unsafe.
	closeErr := binding.subject.loop.quiesce(closeContext)
	if routeRegistration != nil {
		routeRegistration.Release()
	}
	if sessionHandle != nil {
		closeErr = errors.Join(
			closeErr,
			sessionHandle.Release(closeContext),
		)
	}
	return closeErr
}

var _ plugin.Plugin = (*sessionBinding)(nil)
