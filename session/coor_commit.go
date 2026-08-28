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
	if err := owner.admit(item); err != nil {
		return WriteResult{}, err
	}
	<-item.ready
	result, err := owner.executeRequest(item)
	owner.queue.complete(item)
	return result, err
}

func (owner *coordinator) admit(item *request) error {
	owner.queue.mutex.Lock()
	defer owner.queue.mutex.Unlock()
	if err := owner.machine.admit(item.kind); err != nil {
		return err
	}
	owner.queue.appendLocked(item)
	return nil
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
