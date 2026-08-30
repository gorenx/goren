package tools

import (
	"context"
	"sync"
	"sync/atomic"
)

const (
	handleActive uint32 = iota
	handleUnregistering
	handleUnregistered
)

type handleState struct {
	phase         atomic.Uint32
	unregisterErr error
}

func (state *handleState) begin() (bool, error) {
	for {
		switch state.phase.Load() {
		case handleUnregistering:
			return false, nil
		case handleUnregistered:
			return false, state.unregisterErr
		default:
			if state.phase.CompareAndSwap(
				handleActive,
				handleUnregistering,
			) {
				return true, nil
			}
		}
	}
}

func (state *handleState) finish(completed bool, unregisterErr error) {
	if !completed {
		state.phase.Store(handleActive)
		return
	}
	state.unregisterErr = unregisterErr
	state.phase.Store(handleUnregistered)
}

// ToolHandle owns one exact Tool definition in one Catalog layer.
type ToolHandle struct {
	state handleState
	owner *ToolLayer
	entry *registeredTool
}

// Unregister removes only the Tool definition represented by this handle.
func (handle *ToolHandle) Unregister(requestContext context.Context) error {
	if handle == nil || handle.owner == nil || handle.entry == nil {
		return nil
	}
	proceed, previousErr := handle.state.begin()
	if !proceed {
		return previousErr
	}
	completed, err := handle.owner.unregisterTool(
		requestContext,
		handle.entry,
	)
	handle.state.finish(completed, err)
	return err
}

// RestrictionHandle owns one exact inherited-Tool visibility restriction.
type RestrictionHandle struct {
	state handleState
	owner *ToolLayer
	name  string
	entry *registeredRestriction
}

// Unregister removes only the restriction represented by this handle.
func (handle *RestrictionHandle) Unregister(requestContext context.Context) error {
	if handle == nil || handle.owner == nil || handle.entry == nil {
		return nil
	}
	proceed, previousErr := handle.state.begin()
	if !proceed {
		return previousErr
	}
	completed, err := handle.owner.unregisterRestriction(
		requestContext,
		handle.name,
		handle.entry,
	)
	handle.state.finish(completed, err)
	return err
}

// GuardHandle owns one exact Tool execution guard.
type GuardHandle struct {
	state handleState
	owner *ToolLayer
	name  string
	entry *registeredGuard
}

// ResultObserverHandle owns one exact plain-ToolLayer result observation.
type ResultObserverHandle struct {
	once     sync.Once
	mutex    sync.RWMutex
	observer ResultObserver
	active   bool
}

// Close removes only the result observer represented by this handle.
func (handle *ResultObserverHandle) Close(context.Context) error {
	if handle == nil {
		return nil
	}
	handle.once.Do(func() {
		handle.mutex.Lock()
		handle.active = false
		handle.observer = nil
		handle.mutex.Unlock()
	})
	return nil
}

func (handle *ResultObserverHandle) observe(
	requestContext context.Context,
	completed ExecutionCompleted,
) error {
	handle.mutex.RLock()
	observer := handle.observer
	active := handle.active
	handle.mutex.RUnlock()
	if !active || observer == nil {
		return nil
	}
	return observer.ObserveToolResult(requestContext, completed)
}

// Unregister removes only the guard represented by this handle.
func (handle *GuardHandle) Unregister(requestContext context.Context) error {
	if handle == nil || handle.owner == nil || handle.entry == nil {
		return nil
	}
	proceed, previousErr := handle.state.begin()
	if !proceed {
		return previousErr
	}
	completed, err := handle.owner.unregisterGuard(
		requestContext,
		handle.name,
		handle.entry,
	)
	handle.state.finish(completed, err)
	return err
}
