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

	mutex       sync.Mutex
	rootHandle  plugin.Handle
	attached    bool
	treeStopped bool
	closing     bool
	closed      chan struct{}
	closeErr    error
}

func newAgentLifecycle(mountOwner *Plugin) *agentLifecycle {
	return &agentLifecycle{
		mountOwner: mountOwner,
		closed:     make(chan struct{}),
	}
}

func (lifecycle *agentLifecycle) attachRoot(rootHandle plugin.Handle) bool {
	lifecycle.mutex.Lock()
	defer lifecycle.mutex.Unlock()
	if lifecycle.treeStopped || lifecycle.closing || rootHandle.ID() == 0 {
		return false
	}
	lifecycle.rootHandle = rootHandle
	lifecycle.attached = true
	return true
}

func (lifecycle *agentLifecycle) markTreeStopped() {
	lifecycle.mutex.Lock()
	lifecycle.treeStopped = true
	lifecycle.mutex.Unlock()
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
	stopped := lifecycle.treeStopped
	attached := lifecycle.attached
	rootHandle := lifecycle.rootHandle
	lifecycle.mutex.Unlock()

	var closeErr error
	if !stopped {
		if !attached {
			closeErr = errors.New("agentloop: Agent tree Handle was not attached")
		} else {
			closeErr = plugin.UnloadChild(
				closeContext,
				lifecycle.mountOwner,
				rootHandle,
			)
			if closeErr != nil {
				lifecycle.mutex.Lock()
				stopped = lifecycle.treeStopped
				lifecycle.mutex.Unlock()
				if stopped {
					closeErr = nil
				}
			}
		}
	}
	lifecycle.mutex.Lock()
	lifecycle.closeErr = closeErr
	close(lifecycle.closed)
	lifecycle.mutex.Unlock()
	return closeErr
}
