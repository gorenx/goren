package agentloop

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/plugin"
)

// agentLifecycle is the caller-facing owner of one Runtime-managed Agent tree.
// Agent membership itself belongs to agentMembership.
type agentLifecycle struct {
	mountOwner plugin.Plugin
	rootHandle plugin.Handle

	mutex          sync.Mutex
	closingStarted bool
	closing        chan struct{}
	closingOnce    sync.Once
	closed         chan struct{}
	closedOnce     sync.Once
	closeErr       error
}

func newAgentLifecycle(
	mountOwner plugin.Plugin,
	rootHandle plugin.Handle,
) *agentLifecycle {
	return &agentLifecycle{
		mountOwner: mountOwner,
		rootHandle: rootHandle,
		closing:    make(chan struct{}),
		closed:     make(chan struct{}),
	}
}

func (lifecycle *agentLifecycle) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	lifecycle.mutex.Lock()
	if lifecycle.closingStarted {
		closed := lifecycle.closed
		lifecycle.mutex.Unlock()
		select {
		case <-closed:
			lifecycle.mutex.Lock()
			closeErr := lifecycle.closeErr
			lifecycle.mutex.Unlock()
			return closeErr
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	}
	lifecycle.closingStarted = true
	lifecycle.closingOnce.Do(func() {
		close(lifecycle.closing)
	})
	rootHandle := lifecycle.rootHandle
	lifecycle.mutex.Unlock()

	closeErr := plugin.UnloadChild(
		context.WithoutCancel(closeContext),
		lifecycle.mountOwner,
		rootHandle,
	)
	if errors.Is(closeErr, plugin.ErrPluginNotActive) ||
		errors.Is(closeErr, plugin.ErrPluginNotBound) {
		closeErr = nil
	}
	lifecycle.complete(closeErr)
	return closeErr
}

func (lifecycle *agentLifecycle) beginStructuralTeardown() {
	if lifecycle == nil {
		return
	}
	lifecycle.mutex.Lock()
	if !lifecycle.closingStarted {
		lifecycle.closingStarted = true
		lifecycle.closingOnce.Do(func() {
			close(lifecycle.closing)
		})
	}
	lifecycle.mutex.Unlock()
}

func (lifecycle *agentLifecycle) ClosingSignal() <-chan struct{} {
	if lifecycle == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return lifecycle.closing
}

func (lifecycle *agentLifecycle) complete(closeErr error) {
	if lifecycle == nil {
		return
	}
	lifecycle.closedOnce.Do(func() {
		lifecycle.mutex.Lock()
		lifecycle.closeErr = closeErr
		close(lifecycle.closed)
		lifecycle.mutex.Unlock()
	})
}
