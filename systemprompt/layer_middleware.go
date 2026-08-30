package systemprompt

import (
	"context"
	"errors"
	"sync"
)

// AssemblyAction resolves one System Prompt assembly stage.
type AssemblyAction interface {
	Execute(context.Context, AssembleRequest) (PromptAssembly, error)
}

// AssemblyMiddleware wraps prompt assembly inside one plain PromptLayer.
type AssemblyMiddleware interface {
	InterceptAssembly(
		context.Context,
		AssembleRequest,
		AssemblyAction,
	) (PromptAssembly, error)
}

// AssemblyMiddlewareHandle owns one ordered PromptLayer-local Middleware binding.
// It does not reference the PromptLayer that retains it.
type AssemblyMiddlewareHandle struct {
	once       sync.Once
	mutex      sync.RWMutex
	middleware AssemblyMiddleware
	active     bool
}

// UseAssembly registers Middleware in call order on this exact PromptLayer.
func (owner *PromptLayer) UseAssembly(
	middleware AssemblyMiddleware,
) (*AssemblyMiddlewareHandle, error) {
	if owner == nil || middleware == nil {
		return nil, errors.New("systemprompt: assembly Middleware is required")
	}
	handle := &AssemblyMiddlewareHandle{
		middleware: middleware,
		active:     true,
	}
	owner.middlewareMutex.Lock()
	owner.middleware = append(owner.middleware, handle)
	owner.middlewareMutex.Unlock()
	return handle, nil
}

// Close disables this exact Middleware binding.
func (handle *AssemblyMiddlewareHandle) Close(context.Context) error {
	if handle == nil {
		return nil
	}
	handle.once.Do(func() {
		handle.mutex.Lock()
		handle.active = false
		handle.middleware = nil
		handle.mutex.Unlock()
	})
	return nil
}

func (handle *AssemblyMiddlewareHandle) middlewareValue() (
	AssemblyMiddleware,
	bool,
) {
	handle.mutex.RLock()
	middleware := handle.middleware
	active := handle.active
	handle.mutex.RUnlock()
	return middleware, active
}

type assemblyEffectsAction struct {
	effects   layerEffects
	candidate PromptAssembly
}

func (action assemblyEffectsAction) Execute(
	requestContext context.Context,
	request AssembleRequest,
) (PromptAssembly, error) {
	return action.effects.ResolveAssembly(
		requestContext,
		request,
		action.candidate,
	)
}

type assemblyMiddlewareAction struct {
	middleware AssemblyMiddleware
	next       AssemblyAction
}

func (action assemblyMiddlewareAction) Execute(
	requestContext context.Context,
	request AssembleRequest,
) (PromptAssembly, error) {
	return action.middleware.InterceptAssembly(
		requestContext,
		request,
		action.next,
	)
}
