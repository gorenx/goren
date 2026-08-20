package plugin

import (
	"context"
	"errors"
	"sync"
)

// RuntimeSettings contains object dependencies used by Event delivery and
// diagnostics. Configuration documents, factories, and catalogs never enter
// the Runtime core.
type RuntimeSettings struct {
	EventFailures EventFailureReporter
}

// Runtime coordinates named lifecycle and registry owners. Lifecycle mutation
// is serialized; typed resolution and dispatch snapshot state under state.
type Runtime struct {
	operations sync.Mutex
	state      sync.RWMutex

	supervisor *fiberSupervisor
	services   *serviceRegistry
	waterfalls *waterfallRegistry
	events     *eventRegistry

	eventFailures EventFailureReporter
}

// NewRuntime creates one isolated Plugin Runtime.
func NewRuntime(settings RuntimeSettings) *Runtime {
	runtimeEngine := &Runtime{
		services:      newServiceRegistry(),
		waterfalls:    newWaterfallRegistry(),
		events:        newEventRegistry(),
		eventFailures: settings.EventFailures,
	}
	runtimeEngine.supervisor = newFiberSupervisor(runtimeEngine)
	return runtimeEngine
}

// Load mounts one root Plugin and settles every mounted Plugin whose hard
// Service dependencies are available.
func (runtimeEngine *Runtime) Load(
	loadContext context.Context,
	instance Plugin,
) (Handle, error) {
	if runtimeEngine == nil {
		return Handle{}, errors.New("plugin: load through nil Runtime")
	}
	return runtimeEngine.supervisor.load(loadContext, nil, nil, instance)
}

// Unload stops the selected Plugin, its Child Fiber tree, and active hard
// dependents before reconciling the remaining mounted Plugins.
func (runtimeEngine *Runtime) Unload(
	stopContext context.Context,
	pluginHandle Handle,
) error {
	if runtimeEngine == nil {
		return errors.New("plugin: unload through nil Runtime")
	}
	return runtimeEngine.supervisor.unload(stopContext, pluginHandle)
}

// Replace prepares a candidate privately and retains the active Fiber unless
// the candidate is ready for an atomic Registry-entry swap.
func (runtimeEngine *Runtime) Replace(
	replaceContext context.Context,
	pluginHandle Handle,
	candidate Plugin,
) error {
	if runtimeEngine == nil {
		return errors.New("plugin: replace through nil Runtime")
	}
	return runtimeEngine.supervisor.replace(
		replaceContext,
		pluginHandle,
		candidate,
	)
}

// Shutdown closes admission and stops every mounted Plugin in owned order.
func (runtimeEngine *Runtime) Shutdown(stopContext context.Context) error {
	if runtimeEngine == nil {
		return nil
	}
	return runtimeEngine.supervisor.shutdown(stopContext)
}

// Status returns immutable diagnostics for one mounted Plugin.
func (runtimeEngine *Runtime) Status(pluginHandle Handle) (FiberStatus, error) {
	if runtimeEngine == nil {
		return FiberStatus{}, errors.New("plugin: status through nil Runtime")
	}
	return runtimeEngine.supervisor.status(pluginHandle)
}

// Statuses returns immutable diagnostics in mount order.
func (runtimeEngine *Runtime) Statuses() []FiberStatus {
	if runtimeEngine == nil {
		return nil
	}
	return runtimeEngine.supervisor.statuses()
}
