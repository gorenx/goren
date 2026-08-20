package plugin

import (
	"errors"
	"sort"
)

// FiberID identifies one concrete activation attempt.
type FiberID uint64

// FiberState is one externally observable lifecycle state.
type FiberState string

const (
	FiberWaiting     FiberState = "waiting-dependencies"
	FiberStarting    FiberState = "starting"
	FiberActive      FiberState = "active"
	FiberStopping    FiberState = "stopping"
	FiberStopped     FiberState = "stopped"
	FiberRollingBack FiberState = "rolling-back"
	FiberFailed      FiberState = "failed"
)

// Handle identifies one mounted Plugin tree root. Runtime owns unloading.
type Handle struct {
	owner *Runtime
	id    uint64
}

func handleOf(runtimeEngine *Runtime, mounted *pluginMount) Handle {
	if runtimeEngine == nil || mounted == nil {
		return Handle{}
	}
	return Handle{
		owner: runtimeEngine,
		id:    mounted.handleID,
	}
}

// ID returns the Runtime-local mount identity.
func (pluginHandle Handle) ID() uint64 {
	return pluginHandle.id
}

// FiberStatus is an immutable lifecycle and binding diagnostic.
type FiberStatus struct {
	HandleID     uint64
	FiberID      FiberID
	Name         string
	State        FiberState
	Dependencies []ServiceDependencyStatus
	Services     []ServiceBindingStatus
	Waterfalls   []WaterfallBindingStatus
	Events       []EventSubscriptionStatus
	Missing      []string
	Error        error
}

// ServiceDependencyStatus describes one resolved activation dependency.
type ServiceDependencyStatus struct {
	Service         string
	ProviderFiberID FiberID
	Optional        bool
}

// ServiceBindingStatus describes one provided Service.
type ServiceBindingStatus struct {
	Service string
	Scope   ScopeKey
}

// WaterfallBindingStatus describes one published Middleware binding.
type WaterfallBindingStatus struct {
	Waterfall string
	Scope     ScopeKey
}

// EventSubscriptionStatus describes one published Observer subscription.
type EventSubscriptionStatus struct {
	Event string
	Scope ScopeKey
}

// Status returns immutable diagnostics for one mounted Plugin.
func (runtimeEngine *Runtime) Status(pluginHandle Handle) (FiberStatus, error) {
	if runtimeEngine == nil {
		return FiberStatus{}, errors.New("plugin: status through nil Runtime")
	}
	mounted, err := runtimeEngine.lookup(pluginHandle)
	if err != nil {
		return FiberStatus{}, err
	}
	runtimeEngine.view.RLock()
	diagnostics := statusOf(mounted)
	runtimeEngine.view.RUnlock()
	return diagnostics, nil
}

// Statuses returns diagnostics in mount order.
func (runtimeEngine *Runtime) Statuses() []FiberStatus {
	if runtimeEngine == nil {
		return nil
	}
	runtimeEngine.view.RLock()
	defer runtimeEngine.view.RUnlock()
	snapshots := make([]FiberStatus, 0, runtimeEngine.mounts.length())
	for _, mounted := range runtimeEngine.mounts.activeMounts() {
		snapshots = append(snapshots, statusOf(mounted))
	}
	return snapshots
}

// Runtime.view must be read-locked by the caller.
func statusOf(mounted *pluginMount) FiberStatus {
	ownerFiber := mounted.current
	if ownerFiber == nil {
		return FiberStatus{
			HandleID: mounted.handleID,
			Name:     mounted.target.manifest.name,
			State:    FiberStopped,
		}
	}
	diagnostics := FiberStatus{
		HandleID: mounted.handleID,
		FiberID:  ownerFiber.id,
		Name:     ownerFiber.target.manifest.name,
		State:    ownerFiber.state,
		Missing:  append([]string(nil), ownerFiber.missing...),
		Error:    ownerFiber.lastError,
	}
	dependencyNames := make([]string, 0, len(ownerFiber.dependencies))
	dependencyByName := make(map[string]*serviceDependency)
	for _, dependency := range ownerFiber.dependencies {
		dependencyNames = append(dependencyNames, dependency.reference.name)
		dependencyByName[dependency.reference.name] = dependency
	}
	sort.Strings(dependencyNames)
	for _, dependencyName := range dependencyNames {
		dependency := dependencyByName[dependencyName]
		diagnostics.Dependencies = append(
			diagnostics.Dependencies,
			ServiceDependencyStatus{
				Service:         dependency.reference.name,
				ProviderFiberID: dependency.binding.owner.id,
				Optional:        dependency.optional,
			},
		)
	}
	if ownerFiber.state != FiberActive {
		return diagnostics
	}
	for _, serviceDeclaration := range ownerFiber.target.manifest.provides {
		diagnostics.Services = append(
			diagnostics.Services,
			ServiceBindingStatus{
				Service: serviceDeclaration.reference.name,
				Scope:   ownerFiber.scope.key,
			},
		)
	}
	for _, eventDeclaration := range ownerFiber.target.manifest.events {
		diagnostics.Events = append(
			diagnostics.Events,
			EventSubscriptionStatus{
				Event: eventDeclaration.reference.name,
				Scope: ownerFiber.scope.key,
			},
		)
	}
	for _, waterfallDeclaration := range ownerFiber.target.manifest.waterfalls {
		diagnostics.Waterfalls = append(
			diagnostics.Waterfalls,
			WaterfallBindingStatus{
				Waterfall: waterfallDeclaration.reference.name,
				Scope:     ownerFiber.scope.key,
			},
		)
	}
	return diagnostics
}
