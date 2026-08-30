package tools

import (
	"context"
	"errors"
	"sync"
)

// ExecuteAction is the Tool-owned terminal contract for one body dispatch.
type ExecuteAction interface {
	Execute(context.Context, ExecuteRequest) (ExecuteOutcome, error)
}

// ExecuteMiddleware wraps Tool body dispatch inside one exact Tool ToolLayer.
type ExecuteMiddleware interface {
	InterceptExecute(
		context.Context,
		ExecuteRequest,
		ExecuteAction,
	) (ExecuteOutcome, error)
}

// ExecuteMiddlewareHandle owns one ordered Tool ToolLayer middleware binding. It
// does not reference the ToolLayer that retains it.
type ExecuteMiddlewareHandle struct {
	once       sync.Once
	mutex      sync.RWMutex
	middleware ExecuteMiddleware
	active     bool
}

// UseExecution registers Middleware in call order on this exact Tool ToolLayer.
func (owner *ToolLayer) UseExecution(
	middleware ExecuteMiddleware,
) (*ExecuteMiddlewareHandle, error) {
	if owner == nil || middleware == nil {
		return nil, errors.New("tools: execution Middleware is required")
	}
	handle := &ExecuteMiddlewareHandle{
		middleware: middleware,
		active:     true,
	}
	owner.executionMutex.Lock()
	owner.executions = append(owner.executions, handle)
	owner.executionMutex.Unlock()
	return handle, nil
}

// Close disables this exact Middleware binding.
func (handle *ExecuteMiddlewareHandle) Close(context.Context) error {
	if handle == nil {
		return nil
	}
	handle.once.Do(func() {
		handle.mutex.Lock()
		handle.middleware = nil
		handle.active = false
		handle.mutex.Unlock()
	})
	return nil
}

func (handle *ExecuteMiddlewareHandle) middlewareValue() (
	ExecuteMiddleware,
	bool,
) {
	handle.mutex.RLock()
	middleware := handle.middleware
	active := handle.active
	handle.mutex.RUnlock()
	return middleware, active
}

type executeMiddlewareAction struct {
	middleware ExecuteMiddleware
	next       ExecuteAction
}

func (action executeMiddlewareAction) Execute(
	requestContext context.Context,
	request ExecuteRequest,
) (ExecuteOutcome, error) {
	return action.middleware.InterceptExecute(
		requestContext,
		request,
		action.next,
	)
}
