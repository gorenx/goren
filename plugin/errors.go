package plugin

import "errors"

var (
	// ErrServiceUnavailable reports that a declared Service dependency has no
	// active provider visible from the Plugin's Scope.
	ErrServiceUnavailable = errors.New("plugin: service is unavailable")
	// ErrServiceConflict reports multiple Service providers in one exact Scope.
	ErrServiceConflict = errors.New("plugin: service provider conflicts with an active provider")
	// ErrPluginNotBound reports use of a Plugin that is not mounted.
	ErrPluginNotBound = errors.New("plugin: Plugin is not bound to a Runtime")
	// ErrPluginNotActive reports an operation that requires an active Plugin.
	ErrPluginNotActive = errors.New("plugin: Plugin is not active")
	// ErrDependencyResolutionClosed reports dependency resolution outside Apply.
	ErrDependencyResolutionClosed = errors.New("plugin: Service dependencies are only resolved during Plugin.Apply")
	// ErrWaterfallAlreadyExecuted reports a Middleware executing the same
	// downstream Action more than once during one call.
	ErrWaterfallAlreadyExecuted = errors.New("plugin: Waterfall Action already executed")
	// ErrTopologyMutation reports a topology change attempted synchronously
	// from Plugin Apply, Dispose, Event, or Waterfall execution.
	ErrTopologyMutation = errors.New("plugin: Plugin callbacks cannot mutate Runtime topology")
)
