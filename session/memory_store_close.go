package session

import (
	"context"
	"errors"
	"fmt"
)

// closeAttempt is one Store Close execution shared by concurrent callers.
type closeAttempt struct {
	// done closes after the attempt records its result.
	done chan struct{}
	// err is the completed result read after done closes.
	err error
}

func (store *memoryStore) Close(closeContext context.Context) error {
	if closeContext == nil {
		return errors.New("session: close Context is nil")
	}
	store.mutex.Lock()
	switch store.phase {
	case storeClosed:
		store.mutex.Unlock()
		return nil
	case storeClosing:
		if store.activeClose != nil {
			attempt := store.activeClose
			store.mutex.Unlock()
			return waitForClose(closeContext, attempt)
		}
	case storeOpen:
	default:
		phase := store.phase
		store.mutex.Unlock()
		return fmt.Errorf("session: unknown Store phase %d", phase)
	}
	store.phase = storeClosing
	attempt := &closeAttempt{
		done: make(chan struct{}),
	}
	store.activeClose = attempt
	active := append([]*liveEntry(nil), store.order...)
	store.mutex.Unlock()

	ownedContext := context.WithoutCancel(closeContext)
	var closeErr error
	for index := len(active) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, store.release(ownedContext, active[index]))
	}
	store.mutex.Lock()
	if store.phase != storeClosing || store.activeClose != attempt {
		attempt.err = errors.Join(
			closeErr,
			errors.New("session: Store Close attempt lost ownership"),
		)
		close(attempt.done)
		store.mutex.Unlock()
		return attempt.err
	}
	attempt.err = closeErr
	store.activeClose = nil
	if closeErr == nil {
		store.phase = storeClosed
	}
	close(attempt.done)
	store.mutex.Unlock()
	return closeErr
}

func waitForClose(closeContext context.Context, attempt *closeAttempt) error {
	select {
	case <-attempt.done:
		return attempt.err
	case <-closeContext.Done():
		return context.Cause(closeContext)
	}
}
