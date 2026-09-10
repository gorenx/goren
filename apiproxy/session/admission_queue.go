package sessionapi

import (
	"context"
	"slices"
	"sync"

	"github.com/gorenx/goren/session"
)

// admissionQueue orders pending API operations, not Agent lifetimes. Empty
// queues are removed; keys carry no address, registration or model authority.
type admissionQueue struct {
	mu      sync.Mutex
	tickets map[session.SessionID][]*ticket
}

type ticket struct {
	ready chan struct{}
}

func (queue *admissionQueue) acquire(ctx context.Context, identifier session.SessionID) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry := &ticket{
		ready: make(chan struct{}),
	}
	queue.mu.Lock()
	if queue.tickets == nil {
		queue.tickets = make(map[session.SessionID][]*ticket)
	}
	queue.tickets[identifier] = append(queue.tickets[identifier], entry)
	if len(queue.tickets[identifier]) == 1 {
		close(entry.ready)
	}
	queue.mu.Unlock()
	release := sync.OnceFunc(func() { queue.remove(identifier, entry) })
	select {
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	case <-entry.ready:
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	}
}

func (queue *admissionQueue) remove(identifier session.SessionID, entry *ticket) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	tickets := queue.tickets[identifier]
	position := slices.Index(tickets, entry)
	if position < 0 {
		return
	}
	tickets = slices.Delete(tickets, position, position+1)
	if len(tickets) == 0 {
		delete(queue.tickets, identifier)
		return
	}
	queue.tickets[identifier] = tickets
	if position == 0 {
		close(tickets[0].ready)
	}
}
