package agentloop

import (
	"context"
	"errors"
	"sync"
)

// constructionGate admits Agent creation while its Agent Loop Plugin is live
// and joins every in-flight Create or Resume operation during teardown.
type constructionGate struct {
	mutex     sync.Mutex
	accepting bool
	lifetime  context.Context
	stop      context.CancelCauseFunc
	inFlight  int
	settled   chan struct{}
}

func newConstructionGate() *constructionGate {
	return &constructionGate{}
}

func (gate *constructionGate) open() error {
	gate.mutex.Lock()
	defer gate.mutex.Unlock()
	if gate.accepting {
		return errors.New("agentloop: Agent lifecycle admission is already active")
	}
	lifetime, stop := context.WithCancelCause(context.Background())
	gate.accepting = true
	gate.lifetime = lifetime
	gate.stop = stop
	gate.inFlight = 0
	gate.settled = make(chan struct{})
	close(gate.settled)
	return nil
}

func (gate *constructionGate) closeAndWait(
	closeContext context.Context,
) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	gate.mutex.Lock()
	gate.accepting = false
	stop := gate.stop
	settled := gate.settled
	gate.mutex.Unlock()
	if stop != nil {
		stop(errors.New("agentloop: Agent Loop is stopping"))
	}
	if settled == nil {
		return nil
	}
	select {
	case <-settled:
		return nil
	case <-closeContext.Done():
		return context.Cause(closeContext)
	}
}

func (gate *constructionGate) begin(
	requestContext context.Context,
) (context.Context, func(), error) {
	if requestContext == nil {
		return nil, nil, errors.New("agentloop: construction Context is nil")
	}
	gate.mutex.Lock()
	if !gate.accepting || gate.lifetime == nil {
		gate.mutex.Unlock()
		return nil, nil, errors.New("agentloop: Agent Loop is not active")
	}
	lifetime := gate.lifetime
	if gate.inFlight == 0 {
		gate.settled = make(chan struct{})
	}
	gate.inFlight++
	gate.mutex.Unlock()

	operationContext, cancelOperation := context.WithCancelCause(requestContext)
	stopFollowing := context.AfterFunc(lifetime, func() {
		cancelOperation(context.Cause(lifetime))
	})
	var finishOnce sync.Once
	completeConstruction := func() {
		finishOnce.Do(func() {
			stopFollowing()
			cancelOperation(nil)
			gate.mutex.Lock()
			gate.inFlight--
			if gate.inFlight == 0 {
				close(gate.settled)
			}
			gate.mutex.Unlock()
		})
	}
	if err := contextFailure(operationContext); err != nil {
		completeConstruction()
		return nil, nil, err
	}
	return operationContext, completeConstruction, nil
}

func (gate *constructionGate) requireOpen() error {
	gate.mutex.Lock()
	accepting := gate.accepting
	gate.mutex.Unlock()
	if !accepting {
		return errors.New("agentloop: Agent Loop is not active")
	}
	return nil
}
