package session

import (
	"context"
	"errors"
	"sync"
)

type afterEventContextKey struct{}

type afterEventQueue struct {
	mu     sync.Mutex
	work   []func()
	closed bool
}

// DeferAfterEvent schedules work after the current committed-event
// publication has completed and LiveStore has released its append reentrancy
// guard. It is available only while observing SessionEventAppended.
func DeferAfterEvent(requestContext context.Context, work func()) error {
	if requestContext == nil {
		return errors.New("session: defer after event with nil Context")
	}
	if work == nil {
		return errors.New("session: defer after event work is nil")
	}
	queue, ok := requestContext.Value(afterEventContextKey{}).(*afterEventQueue)
	if !ok || queue == nil {
		return errors.New("session: defer after event is available only during event publication")
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.closed {
		return errors.New("session: event publication has already completed")
	}
	queue.work = append(queue.work, work)
	return nil
}

func (queue *afterEventQueue) context() context.Context {
	return context.WithValue(context.Background(), afterEventContextKey{}, queue)
}

func (queue *afterEventQueue) run() error {
	queue.mu.Lock()
	queue.closed = true
	tasks := append([]func(){}, queue.work...)
	queue.work = nil
	queue.mu.Unlock()
	var taskErr error
	for _, task := range tasks {
		taskErr = errors.Join(taskErr, safelyDispatch(func() error {
			task()
			return nil
		}))
	}
	return taskErr
}
