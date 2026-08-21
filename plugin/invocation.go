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
	releaseContext context.CancelCauseFunc
	releaseCalls   func()
}

func newInvocationLease(
	requestContext context.Context,
	releaseContext context.CancelCauseFunc,
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
			lease.state.releaseContext(context.Canceled)
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
	active        map[*fiberCall]struct{}
	drained       chan struct{}
	drainedClosed bool
}

type fiberCall struct {
	cancel context.CancelCauseFunc
}

func newFiberCallGate() *fiberCallGate {
	return &fiberCallGate{
		drainedClosed: true,
	}
}

func (gate *fiberCallGate) open() {
	gate.mutex.Lock()
	defer gate.mutex.Unlock()
	gate.accepting = true
	gate.active = nil
	gate.drained = make(chan struct{})
	gate.drainedClosed = false
}

func (gate *fiberCallGate) acquire(admittedCall *fiberCall) bool {
	gate.mutex.Lock()
	defer gate.mutex.Unlock()
	if !gate.accepting {
		return false
	}
	if gate.active == nil {
		gate.active = make(map[*fiberCall]struct{})
	}
	gate.active[admittedCall] = struct{}{}
	return true
}

func (gate *fiberCallGate) release(admittedCall *fiberCall) {
	gate.mutex.Lock()
	delete(gate.active, admittedCall)
	gate.closeDrainedLocked()
	gate.mutex.Unlock()
}

func (gate *fiberCallGate) close() {
	gate.mutex.Lock()
	gate.accepting = false
	for admittedCall := range gate.active {
		admittedCall.cancel(ErrPluginNotActive)
	}
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
	if gate.accepting || len(gate.active) != 0 || gate.drainedClosed {
		return
	}
	close(gate.drained)
	gate.drainedClosed = true
}

func acquireFiberCalls(
	admittedCall *fiberCall,
	fibers ...*fiber,
) (func(), bool) {
	acquired := fibers[:0]
	for _, running := range fibers {
		if running == nil {
			continue
		}
		if containsFiber(acquired, running) {
			continue
		}
		if running.state != FiberActive || !running.calls.acquire(admittedCall) {
			for acquiredIndex := len(acquired) - 1; acquiredIndex >= 0; acquiredIndex-- {
				acquired[acquiredIndex].calls.release(admittedCall)
			}
			return nil, false
		}
		acquired = append(acquired, running)
	}
	return func() {
		for acquiredIndex := len(acquired) - 1; acquiredIndex >= 0; acquiredIndex-- {
			acquired[acquiredIndex].calls.release(admittedCall)
		}
	}, true
}

func containsFiber(fibers []*fiber, candidate *fiber) bool {
	for _, selected := range fibers {
		if selected == candidate {
			return true
		}
	}
	return false
}

// invocationContext creates the shared cancellation context that the caller
// registers with every participating Fiber call gate before releasing
// Runtime.view. Closing any one of those gates cancels the dispatch before the
// gate waits for it to drain.
func (runtimeEngine *Runtime) invocationContext(
	requestContext context.Context,
) (context.Context, context.CancelCauseFunc) {
	callContext, cancelInvocation := context.WithCancelCause(
		runtimeEngine.callbackContext(requestContext),
	)
	return callContext, cancelInvocation
}
