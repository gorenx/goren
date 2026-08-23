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
	mountOwner *Plugin
	rootHandle plugin.Handle

	mutex    sync.Mutex
	closing  bool
	closed   chan struct{}
	closeErr error
}

func newAgentLifecycle(
	mountOwner *Plugin,
	rootHandle plugin.Handle,
) *agentLifecycle {
	return &agentLifecycle{
		mountOwner: mountOwner,
		rootHandle: rootHandle,
		closed:     make(chan struct{}),
	}
}

func (lifecycle *agentLifecycle) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	lifecycle.mutex.Lock()
	if lifecycle.closing {
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
	lifecycle.closing = true
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
	lifecycle.mutex.Lock()
	lifecycle.closeErr = closeErr
	close(lifecycle.closed)
	lifecycle.mutex.Unlock()
	return closeErr
}
