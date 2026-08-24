package session

import (
	"context"
	"errors"
)

// Commit admits one plan to this Session's sole FIFO. The plan builds against
// the snapshot at the queue head; its complete batch is validated before any
// event commits, then published in sequence order after the log lock is released.
func (owner *coordinator) Commit(
	requestContext context.Context,
	plan WritePlan,
) (WriteResult, error) {
	if owner == nil {
		return WriteResult{}, errors.New("session: write to nil Session")
	}
	if requestContext == nil {
		return WriteResult{}, errors.New("session: write Context is nil")
	}
	if plan == nil {
		return WriteResult{}, errors.New("session: WritePlan is nil")
	}
	if err := contextCause(requestContext); err != nil {
		return WriteResult{}, err
	}
	if active, found := requestContext.Value(reentryKey{}).(*coordinator); found &&
		active == owner {
		return WriteResult{}, ErrWriteReentry
	}
	item := &request{
		kind:           requestCommit,
		requestContext: requestContext,
		plan:           plan,
		ready:          make(chan struct{}),
	}
	if err := owner.queue.admit(item); err != nil {
		return WriteResult{}, err
	}
	<-item.ready
	result, err := owner.executeRequest(item)
	owner.queue.complete(item)
	return result, err
}

// sealWrites closes admission, drains admitted plans, and returns the final
// committed prefix. Calling it again returns the same closed prefix.
func (owner *coordinator) sealWrites(requestContext context.Context) (WriteBarrier, error) {
	if owner == nil {
		return WriteBarrier{}, errors.New("session: seal nil Session")
	}
	if requestContext == nil {
		return WriteBarrier{}, errors.New("session: seal Context is nil")
	}
	if active, found := requestContext.Value(reentryKey{}).(*coordinator); found &&
		active == owner {
		return WriteBarrier{}, ErrWriteReentry
	}

	closed, err := owner.queue.seal()
	if err != nil {
		return WriteBarrier{}, err
	}

	select {
	case <-closed:
		return owner.log.currentBarrier(), nil
	case <-requestContext.Done():
		return WriteBarrier{}, context.Cause(requestContext)
	}
}

func (owner *coordinator) orderedBarrier(
	requestContext context.Context,
) (WriteBarrier, error) {
	if owner == nil {
		return WriteBarrier{}, errors.New("session: barrier on nil Session")
	}
	if requestContext == nil {
		return WriteBarrier{}, errors.New("session: barrier Context is nil")
	}
	if err := contextCause(requestContext); err != nil {
		return WriteBarrier{}, err
	}
	if active, found := requestContext.Value(reentryKey{}).(*coordinator); found &&
		active == owner {
		return WriteBarrier{}, ErrWriteReentry
	}
	item := &request{
		kind:           requestBarrier,
		requestContext: requestContext,
		ready:          make(chan struct{}),
	}
	if err := owner.queue.admit(item); err != nil {
		return WriteBarrier{}, err
	}
	<-item.ready
	result, err := owner.executeRequest(item)
	owner.queue.complete(item)
	return result.Barrier, err
}

func contextCause(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("session: Context is nil")
	}
	if requestContext.Err() == nil {
		return nil
	}
	return context.Cause(requestContext)
}
