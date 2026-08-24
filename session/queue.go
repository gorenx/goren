package session

import (
	"context"
	"fmt"
	"sync"
)

type queueState uint8

const (
	queueOpen queueState = iota
	queueSealing
	queueClosed
)

type requestKind uint8

const (
	requestCommit requestKind = iota
	requestBarrier
)

type request struct {
	kind           requestKind
	requestContext context.Context
	plan           WritePlan
	ready          chan struct{}
}

// requestQueue owns admission order and transfers execution to each FIFO head.
type requestQueue struct {
	mutex  sync.Mutex
	state  queueState
	items  []*request
	head   int
	closed chan struct{}
}

func (queue *requestQueue) admit(item *request) error {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	if queue.state != queueOpen {
		return ErrWritesClosed
	}
	wake := queue.head == len(queue.items)
	queue.items = append(queue.items, item)
	if wake {
		close(item.ready)
	}
	return nil
}

func (queue *requestQueue) complete(item *request) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	if queue.head >= len(queue.items) || queue.items[queue.head] != item {
		panic("session: write queue completed a request outside FIFO order")
	}
	queue.items[queue.head] = nil
	queue.head++
	if queue.head < len(queue.items) {
		close(queue.items[queue.head].ready)
		return
	}
	queue.items = nil
	queue.head = 0
	if queue.state == queueSealing {
		queue.closeLocked()
	}
}

func (queue *requestQueue) seal() (<-chan struct{}, error) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	if queue.closed == nil {
		queue.closed = make(chan struct{})
	}
	switch queue.state {
	case queueOpen:
		queue.state = queueSealing
		if queue.head == len(queue.items) {
			queue.closeLocked()
		}
	case queueSealing, queueClosed:
	default:
		return nil, fmt.Errorf("session: invalid write queue state")
	}
	return queue.closed, nil
}

func (queue *requestQueue) closeLocked() {
	if queue.state == queueClosed {
		return
	}
	queue.state = queueClosed
	if queue.closed == nil {
		queue.closed = make(chan struct{})
	}
	close(queue.closed)
}
