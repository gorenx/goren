package plugin

import (
	"context"
	"sync"
)

// InvocationLease retains Runtime admission and the linked Fiber lifetimes for
// an operation whose result continues doing work after its method returns.
// Release is idempotent.
type InvocationLease struct {
	state *invocationLeaseState
}

type invocationLeaseState struct {
	requestContext context.Context
	releaseOnce    sync.Once
	releaseContext func()
	releaseCalls   func()
}

func newInvocationLease(
	requestContext context.Context,
	releaseContext func(),
	releaseCalls func(),
) *InvocationLease {
	return &InvocationLease{
		state: &invocationLeaseState{
			requestContext: requestContext,
			releaseContext: releaseContext,
			releaseCalls:   releaseCalls,
		},
	}
}

// Context is cancelled when the caller or any participating Plugin stops.
func (lease *InvocationLease) Context() context.Context {
	if lease == nil || lease.state == nil || lease.state.requestContext == nil {
		return context.Background()
	}
	return lease.state.requestContext
}

// Release ends the retained invocation and allows participating Fibers to
// drain. The invocation Context is cancelled before call admission is released.
func (lease *InvocationLease) Release() {
	if lease == nil || lease.state == nil {
		return
	}
	lease.state.releaseOnce.Do(func() {
		if lease.state.releaseContext != nil {
			lease.state.releaseContext()
		}
		if lease.state.releaseCalls != nil {
			lease.state.releaseCalls()
		}
	})
}

// fiberCallGate is Runtime-private dispatch admission for one active Fiber.
type fiberCallGate struct {
	mutex         sync.Mutex
	accepting     bool
	active        int
	drained       chan struct{}
	drainedClosed bool
}

func newFiberCallGate() *fiberCallGate {
	drained := make(chan struct{})
	close(drained)
	return &fiberCallGate{
		drained:       drained,
		drainedClosed: true,
	}
}

func (gate *fiberCallGate) open() {
	gate.mutex.Lock()
	defer gate.mutex.Unlock()
	gate.accepting = true
	gate.drained = make(chan struct{})
	gate.drainedClosed = false
}

func (gate *fiberCallGate) acquire() bool {
	gate.mutex.Lock()
	defer gate.mutex.Unlock()
	if !gate.accepting {
		return false
	}
	gate.active++
	return true
}

func (gate *fiberCallGate) release() {
	gate.mutex.Lock()
	if gate.active > 0 {
		gate.active--
	}
	gate.closeDrainedLocked()
	gate.mutex.Unlock()
}

func (gate *fiberCallGate) close() {
	gate.mutex.Lock()
	gate.accepting = false
	gate.closeDrainedLocked()
	gate.mutex.Unlock()
}

func (gate *fiberCallGate) wait(requestContext context.Context) error {
	gate.mutex.Lock()
	drained := gate.drained
	gate.mutex.Unlock()
	select {
	case <-drained:
		return nil
	case <-requestContext.Done():
		return context.Cause(requestContext)
	}
}

func (gate *fiberCallGate) closeDrainedLocked() {
	if gate.accepting || gate.active != 0 || gate.drainedClosed {
		return
	}
	close(gate.drained)
	gate.drainedClosed = true
}

func acquireFiberCalls(fibers ...*fiber) (func(), bool) {
	acquired := make([]*fiber, 0, len(fibers))
	seen := make(map[*fiber]struct{}, len(fibers))
	for _, running := range fibers {
		if running == nil {
			continue
		}
		if _, duplicate := seen[running]; duplicate {
			continue
		}
		seen[running] = struct{}{}
		if running.state != FiberActive || !running.calls.acquire() {
			for acquiredIndex := len(acquired) - 1; acquiredIndex >= 0; acquiredIndex-- {
				acquired[acquiredIndex].calls.release()
			}
			return func() {}, false
		}
		acquired = append(acquired, running)
	}
	return func() {
		for acquiredIndex := len(acquired) - 1; acquiredIndex >= 0; acquiredIndex-- {
			acquired[acquiredIndex].calls.release()
		}
	}, true
}

// invocationContext links one admitted dispatch to every participating Fiber
// lifetime. Runtime cancellation can therefore release long-running handlers
// before their call gates are drained and Dispose is invoked.
//
// Caller holds Runtime.view while participant lifetimes are captured.
func (runtimeEngine *Runtime) invocationContext(
	requestContext context.Context,
	participants ...*fiber,
) (context.Context, func()) {
	callContext, cancelInvocation := context.WithCancelCause(
		runtimeEngine.callbackContext(requestContext),
	)
	stopCallbacks := make([]func() bool, 0, len(participants))
	seen := make(map[*fiber]struct{}, len(participants))
	for _, running := range participants {
		if running == nil || running.lifetime == nil {
			continue
		}
		if _, duplicate := seen[running]; duplicate {
			continue
		}
		seen[running] = struct{}{}
		fiberLifetime := running.lifetime
		stopCallbacks = append(
			stopCallbacks,
			context.AfterFunc(fiberLifetime, func() {
				cause := context.Cause(fiberLifetime)
				if cause == nil {
					cause = ErrPluginNotActive
				}
				cancelInvocation(cause)
			}),
		)
	}
	return callContext, func() {
		for _, stopCallback := range stopCallbacks {
			stopCallback()
		}
		cancelInvocation(context.Canceled)
	}
}
