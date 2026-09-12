package mcpclient

import (
	"bytes"
	"context"
	"errors"
)

// Drain pauses admission and waits for admitted operations without canceling them.
// Cancellation stops only the wait; admission remains paused until Resume or Close.
// Management callers serialize Drain, Resume and Close; tool calls remain concurrent.
func (owner *transport) Drain(ctx context.Context) error {
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
func (owner *transport) Resume() error {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.closed || owner.lifetime.Err() != nil {
		return errors.New("mcpclient: connection is unavailable")
	}
	owner.paused = false
	return nil
}

func (owner *transport) begin(ctx context.Context) (context.Context, func(), error) {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	return owner.admit(ctx)
}

func (owner *transport) beginTool(ctx context.Context, expected remoteTool) (context.Context, func(), error) {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	entry, exists := owner.entries[expected.schema.Name]
	if !exists || entry.rawName != expected.rawName || entry.taskRequired != expected.taskRequired ||
		entry.schema.Description != expected.schema.Description || !bytes.Equal(entry.schema.Parameters, expected.schema.Parameters) {
		return nil, nil, errors.New("mcpclient: tool definition changed; tools must apply the current directory")
	}
	return owner.admit(ctx)
}

// admit requires mutex and captures the transport lifetime before releasing admission.
func (owner *transport) admit(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, errors.New("mcpclient: operation context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
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
