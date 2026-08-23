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
	if !lifecycle.beginClosing() {
		closed := lifecycle.closed
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
	rootHandle := lifecycle.rootHandle

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

func (lifecycle *agentLifecycle) beginClosing() bool {
	if lifecycle == nil {
		return false
	}
	lifecycle.mutex.Lock()
	defer lifecycle.mutex.Unlock()
	if lifecycle.closingStarted {
		return false
	}
	lifecycle.closingStarted = true
	close(lifecycle.closing)
	return true
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
