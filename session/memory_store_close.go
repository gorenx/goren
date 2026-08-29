package session

import (
	"context"
	"errors"
)

func (store *memoryStore) Close(closeContext context.Context) error {
	if closeContext == nil {
		return errors.New("session: close Context is nil")
	}
	store.mutex.Lock()
	if store.state == memoryStoreClosed {
		store.mutex.Unlock()
		return nil
	}
	if store.state == memoryStoreClosing && store.closeDone != nil {
		done := store.closeDone
		store.mutex.Unlock()
		select {
		case <-done:
			store.mutex.RLock()
			closeErr := store.closeErr
			store.mutex.RUnlock()
			return closeErr
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	}
	store.state = memoryStoreClosing
	done := make(chan struct{})
	store.closeDone = done
	active := append([]*liveEntry(nil), store.order...)
	store.mutex.Unlock()

	ownedContext := context.WithoutCancel(closeContext)
	var closeErr error
	for index := len(active) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, store.release(ownedContext, active[index]))
	}
	store.mutex.Lock()
	store.closeErr = closeErr
	store.closeDone = nil
	if closeErr == nil {
		store.state = memoryStoreClosed
	}
	close(done)
	store.mutex.Unlock()
	return closeErr
}
