package plugin

import (
	"context"
	"errors"
	"fmt"
)

// fiber is one runtime activation of one Plugin target.
type fiber struct {
	id      FiberID
	runtime *Runtime
	mount   *pluginMount
	target  pluginTarget
	scope   *scope
	state   FiberState

	dependencies dependencySnapshot
	missing      []string
	lifetime     context.Context
	cancel       context.CancelCauseFunc
	bindings     activationBindings
	calls        *fiberCallGate
	attached     bool
	lastError    error
}

func (running *fiber) prepare(
	applyContext context.Context,
	resolution dependencyResolution,
) error {
	fiberLifetime, cancelLifetime := context.WithCancelCause(context.Background())
	if err := running.target.instance.RuntimePlugin().attach(running); err != nil {
		cancelLifetime(err)
		running.runtime.view.Lock()
		running.state = FiberFailed
		running.lastError = err
		running.runtime.view.Unlock()
		return err
	}
	running.runtime.view.Lock()
	running.lifetime = fiberLifetime
	running.cancel = cancelLifetime
	running.dependencies = resolution.selected
	running.missing = nil
	running.lastError = nil
	running.attached = true
	running.state = FiberStarting
	running.runtime.view.Unlock()
	return invokePluginApply(
		running.runtime.callbackContext(applyContext),
		running.target.instance,
	)
}

// activate publishes one prepared Fiber into the dispatch view. Caller holds
// Runtime.view for the complete binding-and-state transaction.
func (running *fiber) activate(published activationBindings) {
	running.bindings = published
	running.calls.open()
	running.state = FiberActive
}

func (running *fiber) rollback(
	rollbackContext context.Context,
	failure error,
) error {
	running.runtime.view.Lock()
	running.state = FiberRollingBack
	running.runtime.bindings.withdraw(running.bindings)
	running.calls.close()
	running.runtime.view.Unlock()

	cleanupErr := running.release(rollbackContext, failure)
	running.runtime.view.Lock()
	running.state = FiberFailed
	running.lastError = errors.Join(failure, cleanupErr)
	running.runtime.view.Unlock()
	return cleanupErr
}

func (running *fiber) stop(stopContext context.Context) error {
	if running == nil {
		return nil
	}
	running.runtime.view.Lock()
	switch running.state {
	case FiberStopped:
		running.runtime.view.Unlock()
		return nil
	case FiberWaiting:
		running.state = FiberStopped
		running.runtime.view.Unlock()
		return nil
	case FiberFailed:
		running.state = FiberStopped
		previousError := running.lastError
		running.runtime.view.Unlock()
		return previousError
	default:
		running.state = FiberStopping
		running.runtime.bindings.withdraw(running.bindings)
		running.calls.close()
	}
	running.runtime.view.Unlock()

	if running.cancel != nil {
		running.cancel(ErrPluginNotActive)
	}
	drainErr := running.calls.wait(stopContext)
	if drainErr != nil {
		_ = running.calls.wait(context.Background())
	}
	disposeErr := running.release(stopContext, ErrPluginNotActive)
	running.runtime.view.Lock()
	running.state = FiberStopped
	running.lastError = disposeErr
	running.runtime.view.Unlock()
	return errors.Join(drainErr, disposeErr)
}

func (running *fiber) release(
	releaseContext context.Context,
	cause error,
) error {
	if !running.attached {
		return nil
	}
	running.attached = false
	if running.cancel != nil {
		running.cancel(cause)
	}
	disposeErr := invokePluginDispose(
		running.runtime.callbackContext(releaseContext),
		running.target.instance,
	)
	running.target.instance.RuntimePlugin().detach(running)
	return disposeErr
}

func invokePluginApply(
	applyContext context.Context,
	pluginInstance Plugin,
) (applyErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applyErr = panicError("Apply", recovered)
		}
	}()
	return pluginInstance.Apply(applyContext)
}

func invokePluginDispose(
	disposeContext context.Context,
	pluginInstance Plugin,
) (disposeErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			disposeErr = errors.Join(
				disposeErr,
				panicError("Dispose", recovered),
			)
		}
	}()
	return pluginInstance.Dispose(disposeContext)
}

func panicError(operation string, recovered any) error {
	if failure, matches := recovered.(error); matches {
		return fmt.Errorf("plugin: Plugin.%s panicked: %w", operation, failure)
	}
	return fmt.Errorf("plugin: Plugin.%s panicked: %v", operation, recovered)
}
