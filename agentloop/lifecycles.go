package agentloop

import (
	"context"
	"errors"
	"sync"
)

// agentLifecycles owns construction admission and the exact set of mounted
// Agent lifecycles. It does not construct Agents or publish membership.
type agentLifecycles struct {
	mutex      sync.Mutex
	accepting  bool
	lifecycles map[*agentLifecycle]struct{}
	lifetime   context.Context
	stop       context.CancelCauseFunc
	inFlight   int
	settled    chan struct{}
}

func newAgentLifecycles() *agentLifecycles {
	return &agentLifecycles{
		lifecycles: make(map[*agentLifecycle]struct{}),
	}
}

func (trees *agentLifecycles) start() error {
	trees.mutex.Lock()
	defer trees.mutex.Unlock()
	if trees.accepting {
		return errors.New("agentloop: Agent lifecycle admission is already active")
	}
	if len(trees.lifecycles) != 0 {
		return errors.New("agentloop: inactive Agent lifecycle set is not empty")
	}
	lifetime, stop := context.WithCancelCause(context.Background())
	trees.accepting = true
	trees.lifetime = lifetime
	trees.stop = stop
	trees.inFlight = 0
	trees.settled = make(chan struct{})
	close(trees.settled)
	return nil
}

func (trees *agentLifecycles) stopAndWait(
	closeContext context.Context,
) (int, error) {
	if closeContext == nil {
		closeContext = context.Background()
	}
	trees.mutex.Lock()
	trees.accepting = false
	stop := trees.stop
	settled := trees.settled
	dangling := len(trees.lifecycles)
	trees.mutex.Unlock()
	if stop != nil {
		stop(errors.New("agentloop: Agent Loop is stopping"))
	}
	select {
	case <-settled:
		return dangling, nil
	case <-closeContext.Done():
		return dangling, context.Cause(closeContext)
	}
}

func (trees *agentLifecycles) beginConstruction(
	requestContext context.Context,
) (context.Context, func(), error) {
	if requestContext == nil {
		return nil, nil, errors.New("agentloop: construction Context is nil")
	}
	trees.mutex.Lock()
	if !trees.accepting || trees.lifetime == nil {
		trees.mutex.Unlock()
		return nil, nil, errors.New("agentloop: Agent Loop is not active")
	}
	lifetime := trees.lifetime
	if trees.inFlight == 0 {
		trees.settled = make(chan struct{})
	}
	trees.inFlight++
	trees.mutex.Unlock()

	operationContext, cancelOperation := context.WithCancelCause(requestContext)
	stopFollowing := context.AfterFunc(lifetime, func() {
		cancelOperation(context.Cause(lifetime))
	})
	var finishOnce sync.Once
	completeConstruction := func() {
		finishOnce.Do(func() {
			stopFollowing()
			cancelOperation(nil)
			trees.mutex.Lock()
			trees.inFlight--
			if trees.inFlight == 0 {
				close(trees.settled)
			}
			trees.mutex.Unlock()
		})
	}
	if err := contextFailure(operationContext); err != nil {
		completeConstruction()
		return nil, nil, err
	}
	return operationContext, completeConstruction, nil
}

func (trees *agentLifecycles) requireAccepting() error {
	trees.mutex.Lock()
	accepting := trees.accepting
	trees.mutex.Unlock()
	if !accepting {
		return errors.New("agentloop: Agent Loop is not active")
	}
	return nil
}

func (trees *agentLifecycles) track(lifecycle *agentLifecycle) error {
	if lifecycle == nil {
		return errors.New("agentloop: Agent lifecycle is nil")
	}
	trees.mutex.Lock()
	defer trees.mutex.Unlock()
	if !trees.accepting {
		return errors.New("agentloop: Agent Loop is not active")
	}
	if _, exists := trees.lifecycles[lifecycle]; exists {
		return errors.New("agentloop: Agent lifecycle is already tracked")
	}
	trees.lifecycles[lifecycle] = struct{}{}
	return nil
}

func (trees *agentLifecycles) forget(lifecycle *agentLifecycle) {
	if lifecycle == nil {
		return
	}
	trees.mutex.Lock()
	delete(trees.lifecycles, lifecycle)
	trees.mutex.Unlock()
}
