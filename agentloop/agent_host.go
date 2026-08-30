package agentloop

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

type sessionPublicationPhase uint8

const (
	// sessionUnpublished means Session Created publication has not begun.
	sessionUnpublished sessionPublicationPhase = iota
	// sessionPublishing means Session Created publication is active.
	sessionPublishing
	// sessionPublished means Session Created publication completed.
	sessionPublished
)

// agentHost owns one RLA, its Session membership, and one Agent Scope. It has
// no Registry, parent, child, or Plugin reference.
type agentHost struct {
	subject     *ReactLoopAgent
	scope       *agentScope
	sessions    session.LiveStore
	preparation *session.Preparation

	mutex              sync.Mutex
	sessionHandle      session.Handle
	sessionPublication sessionPublicationPhase
	closeOnce          sync.Once
	closeDone          chan struct{}
	closeErr           error
}

func newAgentHost(
	subject *ReactLoopAgent,
	scopeState *agentScope,
	sessions session.LiveStore,
	preparation *session.Preparation,
) *agentHost {
	return &agentHost{
		subject:     subject,
		scope:       scopeState,
		sessions:    sessions,
		preparation: preparation,
		closeDone:   make(chan struct{}),
	}
}

func (owner *agentHost) Agent() agent.Agent {
	if owner == nil {
		return nil
	}
	return owner.subject
}

func (owner *agentHost) Scope() agent.Scope {
	if owner == nil {
		return nil
	}
	return owner.scope
}

func (owner *agentHost) EnterServing(requestContext context.Context) error {
	if owner == nil || owner.subject == nil || owner.sessions == nil {
		return errors.New("agentloop: Agent Host is incomplete")
	}
	owner.mutex.Lock()
	if owner.sessionHandle != nil {
		owner.mutex.Unlock()
		return errors.New("agentloop: Agent is already serving")
	}
	owner.mutex.Unlock()
	sessionHandle, err := owner.sessions.Enter(owner.subject.SessionValue())
	if err != nil {
		return err
	}
	owner.mutex.Lock()
	owner.sessionHandle = sessionHandle
	owner.mutex.Unlock()
	if err = owner.subject.enterServing(); err != nil {
		return errors.Join(err, owner.Close(context.WithoutCancel(requestContext)))
	}
	return requestContext.Err()
}

func (owner *agentHost) Announce(requestContext context.Context) error {
	owner.mutex.Lock()
	if owner.sessionPublication != sessionUnpublished {
		owner.mutex.Unlock()
		return errors.New("agentloop: Session binding was already announced")
	}
	owner.sessionPublication = sessionPublishing
	owner.mutex.Unlock()
	err := owner.sessions.Announce(requestContext, owner.subject.SessionValue())
	owner.mutex.Lock()
	if err != nil {
		owner.sessionPublication = sessionUnpublished
	} else {
		owner.sessionPublication = sessionPublished
	}
	owner.releasePreparationLocked()
	owner.mutex.Unlock()
	return err
}

func (owner *agentHost) Close(closeContext context.Context) error {
	if owner == nil {
		return nil
	}
	if closeContext == nil {
		closeContext = context.Background()
	}
	owner.closeOnce.Do(func() {
		completionContext := context.WithoutCancel(closeContext)
		if err := owner.subject.shutdown(completionContext); err != nil {
			owner.closeErr = errors.Join(
				owner.closeErr,
				fmt.Errorf("agentloop: shut down RLA: %w", err),
			)
		}
		owner.mutex.Lock()
		sessionHandle := owner.sessionHandle
		owner.sessionHandle = nil
		owner.releasePreparationLocked()
		owner.mutex.Unlock()
		if sessionHandle != nil {
			owner.closeErr = errors.Join(
				owner.closeErr,
				sessionHandle.Release(completionContext),
			)
		}
		owner.closeErr = errors.Join(
			owner.closeErr,
			owner.scope.Close(completionContext),
		)
		close(owner.closeDone)
	})
	select {
	case <-owner.closeDone:
		return owner.closeErr
	case <-closeContext.Done():
		return context.Cause(closeContext)
	}
}

func (owner *agentHost) releasePreparationLocked() {
	if owner.preparation != nil {
		owner.preparation.Dispose()
		owner.preparation = nil
	}
}

var _ agent.Host = (*agentHost)(nil)
var _ agent.Setup = agentVariablesSetup{}
