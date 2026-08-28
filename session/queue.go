package session

import (
	"context"
	"sync"
)

type requestKind uint8

const (
	requestCommit requestKind = iota
	requestEnter
	requestAnnounce
	requestFlush
	requestRelease
)

type request struct {
	kind           requestKind
	requestContext context.Context
	plan           WritePlan
	store          *memoryStore
	ready          chan struct{}
}

// requestQueue owns admission order and transfers execution to each FIFO head.
type requestQueue struct {
	mutex sync.Mutex
	items []*request
	head  int
}

func (queue *requestQueue) appendLocked(item *request) {
	wake := queue.head == len(queue.items)
	queue.items = append(queue.items, item)
	if wake {
		close(item.ready)
	}
}

func (queue *requestQueue) complete(item *request) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	queue.completeLocked(item)
}

func (queue *requestQueue) completeLocked(item *request) {
	if queue.head >= len(queue.items) || queue.items[queue.head] != item {
		panic("session: operation queue completed a request outside FIFO order")
	}
	queue.items[queue.head] = nil
	queue.head++
	if queue.head < len(queue.items) {
		close(queue.items[queue.head].ready)
		return
	}
	queue.items = nil
	queue.head = 0
}
