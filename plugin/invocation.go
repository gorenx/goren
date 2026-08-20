package plugin

import (
	"context"
	"sync"
)

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
