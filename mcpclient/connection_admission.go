package mcpclient

import (
	"context"
	"errors"
)

// ErrDraining means the owner has paused this target for configuration replacement.
var ErrDraining = errors.New("mcpclient: connection is draining for configuration update")

// Drain pauses new operations and waits for calls already using the target.
// A canceled wait leaves admission paused until Resume or Close.
func (owner *Connection) Drain(ctx context.Context) error {
	_, finish, err := owner.operation(ctx)
	if err != nil {
		return err
	}
	defer finish()
	owner.mutex.Lock()
	owner.paused = true
	peer := owner.current
	owner.mutex.Unlock()
	if err := peer.Drain(ctx); err != nil {
		if owner.lifetime.Err() != nil {
			return peer.Close(ctx)
		}
		return err
	}
	return owner.releaseRetired(ctx, true)
}

// Resume permits operations on the existing healthy transport without reconnecting.
func (owner *Connection) Resume() error {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.closed {
		return errors.New("mcpclient: connection is unavailable")
	}
	if err := owner.current.Resume(); err != nil {
		return err
	}
	owner.paused = false
	return nil
}

// beginTool captures one transport and validates the installed definition before
// admitting I/O. Reconnect cannot move an already admitted operation to a new peer.
func (owner *Connection) beginTool(ctx context.Context, expected remoteTool) (*transport, context.Context, func(), error) {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.closed {
		return nil, nil, nil, errors.New("mcpclient: connection is unavailable")
	}
	if owner.paused {
		return nil, nil, nil, ErrDraining
	}
	peer := owner.current
	work, finish, err := peer.beginTool(ctx, expected)
	return peer, work, finish, err
}
