package mcpclient

import (
	"context"
	"errors"
)

// ErrDraining means configuration replacement has paused new connection operations.
// The manager must explicitly resume admission or close the connection.
var ErrDraining = errors.New("mcpclient: connection is draining for configuration update")

// Drain pauses admission and waits for admitted operations without canceling them.
// Cancellation stops only the wait; admission remains paused until Resume or Close.
// Management callers serialize Drain, Resume and Close; tool calls remain concurrent.
func (owner *Connection) Drain(ctx context.Context) error {
	if ctx == nil {
		return errors.New("mcpclient: drain context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	owner.mutex.Lock()
	if owner.closed || owner.lifetime.Err() != nil {
		owner.mutex.Unlock()
		return errors.New("mcpclient: connection is unavailable")
	}
	owner.paused = true
	idle := owner.idle
	owner.mutex.Unlock()
	if idle == nil {
		return nil
	}
	select {
	case <-idle:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Resume reopens admission after a replacement was abandoned, without reconnecting.
func (owner *Connection) Resume() error {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.closed || owner.lifetime.Err() != nil {
		return errors.New("mcpclient: connection is unavailable")
	}
	owner.paused = false
	return nil
}

func (owner *Connection) begin(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, errors.New("mcpclient: operation context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.closed || owner.lifetime.Err() != nil {
		return nil, nil, errors.New("mcpclient: connection is unavailable")
	}
	if owner.paused {
		return nil, nil, ErrDraining
	}
	work, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(owner.lifetime, cancel)
	if owner.active == 0 {
		owner.idle = make(chan struct{})
	}
	owner.active++
	return work, func() {
		stop()
		cancel()
		owner.mutex.Lock()
		owner.active--
		if owner.active == 0 {
			close(owner.idle)
		}
		owner.mutex.Unlock()
	}, nil
}
