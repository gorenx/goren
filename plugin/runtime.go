package plugin

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// RuntimeSettings contains Runtime-owned technical collaborators.
type RuntimeSettings struct {
	EventFailures EventFailureReporter
}

// Runtime is the serialized command facade for one isolated Plugin system.
// Its collaborators own topology, dispatch bindings, dependency queries, and
// cross-Fiber activation ordering respectively.
type Runtime struct {
	operations sync.Mutex
	view       sync.RWMutex

	mounts       *mountTree
	bindings     *runtimeBindings
	dependencies *dependencyGraph
	activations  *activationCoordinator

	started bool
	closed  bool
}

// NewRuntime creates one isolated Plugin Runtime.
func NewRuntime(settings RuntimeSettings) *Runtime {
	runtimeEngine := &Runtime{
		mounts:   newMountTree(),
		bindings: newRuntimeBindings(settings.EventFailures),
	}
	runtimeEngine.dependencies = newDependencyGraph(
		runtimeEngine,
		runtimeEngine.mounts,
		runtimeEngine.bindings.services,
	)
	runtimeEngine.activations = newActivationCoordinator(
		runtimeEngine,
		runtimeEngine.mounts,
		runtimeEngine.dependencies,
	)
	return runtimeEngine
}

// Start atomically admits the complete static Plugin set and returns only when
// every Plugin is active. Failure rolls the whole batch back in reverse order.
func (runtimeEngine *Runtime) Start(
	startContext context.Context,
	instances ...Plugin,
) ([]Handle, error) {
	if runtimeEngine == nil {
		return nil, errors.New("plugin: start through nil Runtime")
	}
	runtimeEngine.operations.Lock()
	defer runtimeEngine.operations.Unlock()
	if runtimeEngine.closed {
		return nil, errors.New("plugin: Runtime is shut down")
	}
	if runtimeEngine.started {
		return nil, errors.New("plugin: Runtime has already started")
	}
	if len(instances) == 0 {
		return nil, errors.New("plugin: Start requires at least one Plugin")
	}

	startIndex := runtimeEngine.mounts.length()
	handles := make([]Handle, 0, len(instances))
	for _, pluginInstance := range instances {
		target, err := newPluginTarget(pluginInstance)
		if err == nil {
			err = runtimeEngine.bindings.validateAdmission(target)
		}
		if err != nil {
			rollbackErr := runtimeEngine.activations.rollbackMounts(
				startContext,
				runtimeEngine.mounts.from(startIndex),
			)
			return nil, errors.Join(err, rollbackErr)
		}
		mounted, err := runtimeEngine.mounts.admit(
			target,
			nil,
			mountAtRootScope,
		)
		if err != nil {
			rollbackErr := runtimeEngine.activations.rollbackMounts(
				startContext,
				runtimeEngine.mounts.from(startIndex),
			)
			return nil, errors.Join(err, rollbackErr)
		}
		runtimeEngine.activations.attach(mounted)
		handles = append(handles, handleOf(runtimeEngine, mounted))
	}
	startedMounts := runtimeEngine.mounts.from(startIndex)
	settleErr := runtimeEngine.activations.converge(startContext)
	readinessErr := runtimeEngine.dependencies.readiness(startedMounts)
	if startErr := errors.Join(settleErr, readinessErr); startErr != nil {
		rollbackErr := runtimeEngine.activations.rollbackMounts(
			startContext,
			startedMounts,
		)
		return nil, errors.Join(startErr, rollbackErr)
	}
	runtimeEngine.started = true
	return handles, nil
}

// Mount adds one root Plugin after Start.
func (runtimeEngine *Runtime) Mount(
	mountContext context.Context,
	pluginInstance Plugin,
) (Handle, error) {
	return runtimeEngine.mount(
		mountContext,
		Handle{},
		pluginInstance,
		mountAtRootScope,
	)
}

// MountChild adds a child Fiber that inherits its parent's Scope.
func (runtimeEngine *Runtime) MountChild(
	mountContext context.Context,
	parent Handle,
	pluginInstance Plugin,
) (Handle, error) {
	return runtimeEngine.mount(
		mountContext,
		parent,
		pluginInstance,
		mountInParentScope,
	)
}

// MountScopedChild adds a child Fiber in a new Scope below its parent.
func (runtimeEngine *Runtime) MountScopedChild(
	mountContext context.Context,
	parent Handle,
	pluginInstance Plugin,
) (Handle, error) {
	return runtimeEngine.mount(
		mountContext,
		parent,
		pluginInstance,
		mountInChildScope,
	)
}

// MountChild adds child as an owned Fiber in activeParent's current Scope.
func MountChild(
	mountContext context.Context,
	activeParent Plugin,
	child Plugin,
) (Handle, error) {
	parentFiber, err := activeFiberOf(activeParent)
	if err != nil {
		return Handle{}, err
	}
	return parentFiber.runtime.mount(
		mountContext,
		handleOf(parentFiber.runtime, parentFiber.mount),
		child,
		mountInParentScope,
	)
}

// MountScopedChild adds child as an owned Fiber in a new Scope below
// activeParent's Scope.
func MountScopedChild(
	mountContext context.Context,
	activeParent Plugin,
	child Plugin,
) (Handle, error) {
	parentFiber, err := activeFiberOf(activeParent)
	if err != nil {
		return Handle{}, err
	}
	return parentFiber.runtime.mount(
		mountContext,
		handleOf(parentFiber.runtime, parentFiber.mount),
		child,
		mountInChildScope,
	)
}

// UnloadChild stops one direct child previously mounted by activeParent.
func UnloadChild(
	stopContext context.Context,
	activeParent Plugin,
	child Handle,
) error {
	parentFiber, err := activeFiberOf(activeParent)
	if err != nil {
		return err
	}
	return parentFiber.runtime.unloadChild(
		stopContext,
		handleOf(parentFiber.runtime, parentFiber.mount),
		child,
	)
}

func (runtimeEngine *Runtime) mount(
	mountContext context.Context,
	parent Handle,
	pluginInstance Plugin,
	placement scopePlacement,
) (Handle, error) {
	if runtimeEngine == nil {
		return Handle{}, errors.New("plugin: mount through nil Runtime")
	}
	runtimeEngine.operations.Lock()
	defer runtimeEngine.operations.Unlock()
	if runtimeEngine.closed {
		return Handle{}, errors.New("plugin: Runtime is shut down")
	}
	if !runtimeEngine.started {
		return Handle{}, errors.New("plugin: Runtime must Start before Mount")
	}

	parentMount, err := runtimeEngine.resolveParentMount(parent, placement)
	if err != nil {
		return Handle{}, err
	}
	target, err := newPluginTarget(pluginInstance)
	if err == nil {
		err = runtimeEngine.bindings.validateAdmission(target)
	}
	if err != nil {
		return Handle{}, err
	}
	mounted, err := runtimeEngine.mounts.admit(
		target,
		parentMount,
		placement,
	)
	if err != nil {
		return Handle{}, err
	}
	runtimeEngine.activations.attach(mounted)
	settleErr := runtimeEngine.activations.converge(mountContext)
	readinessErr := runtimeEngine.dependencies.readiness([]*pluginMount{mounted})
	if mountErr := errors.Join(settleErr, readinessErr); mountErr != nil {
		rollbackErr := runtimeEngine.activations.removeMount(
			mountContext,
			mounted,
		)
		reconcileErr := runtimeEngine.activations.converge(mountContext)
		return Handle{}, errors.Join(mountErr, rollbackErr, reconcileErr)
	}
	return handleOf(runtimeEngine, mounted), nil
}

func (runtimeEngine *Runtime) resolveParentMount(
	parent Handle,
	placement scopePlacement,
) (*pluginMount, error) {
	if placement == mountAtRootScope {
		if parent.owner != nil || parent.id != 0 {
			return nil, errors.New("plugin: root mount cannot have a parent Handle")
		}
		return nil, nil
	}
	mounted, err := runtimeEngine.lookup(parent)
	if err != nil {
		return nil, err
	}
	runtimeEngine.view.RLock()
	active := mounted.current != nil && mounted.current.state == FiberActive
	runtimeEngine.view.RUnlock()
	if !active {
		return nil, errors.New("plugin: parent Plugin is not active")
	}
	return mounted, nil
}

// Unload stops the selected Plugin, its owned child tree, and hard dependents.
// Dependents remain mounted and are reconciled against any remaining provider.
func (runtimeEngine *Runtime) Unload(
	stopContext context.Context,
	pluginHandle Handle,
) error {
	if runtimeEngine == nil {
		return errors.New("plugin: unload through nil Runtime")
	}
	runtimeEngine.operations.Lock()
	defer runtimeEngine.operations.Unlock()
	mounted, err := runtimeEngine.lookup(pluginHandle)
	if err != nil {
		return err
	}
	stopErr := runtimeEngine.activations.removeMount(stopContext, mounted)
	settleErr := runtimeEngine.activations.converge(stopContext)
	return errors.Join(stopErr, settleErr)
}

func (runtimeEngine *Runtime) unloadChild(
	stopContext context.Context,
	parent Handle,
	child Handle,
) error {
	if runtimeEngine == nil {
		return errors.New("plugin: unload child through nil Runtime")
	}
	runtimeEngine.operations.Lock()
	defer runtimeEngine.operations.Unlock()
	parentMount, err := runtimeEngine.lookup(parent)
	if err != nil {
		return err
	}
	childMount, err := runtimeEngine.lookup(child)
	if err != nil {
		return err
	}
	if childMount.parent != parentMount {
		return errors.New("plugin: Handle is not a direct child of the owning Plugin")
	}
	stopErr := runtimeEngine.activations.removeMount(stopContext, childMount)
	settleErr := runtimeEngine.activations.converge(stopContext)
	return errors.Join(stopErr, settleErr)
}

// Replace prepares a contract-compatible candidate while the current Plugin
// remains active, then swaps Runtime bindings and reactivates dependents.
func (runtimeEngine *Runtime) Replace(
	replaceContext context.Context,
	pluginHandle Handle,
	candidate Plugin,
) error {
	if runtimeEngine == nil {
		return errors.New("plugin: replace through nil Runtime")
	}
	runtimeEngine.operations.Lock()
	defer runtimeEngine.operations.Unlock()
	mounted, err := runtimeEngine.lookup(pluginHandle)
	if err != nil {
		return err
	}
	target, err := newPluginTarget(candidate)
	if err == nil {
		err = runtimeEngine.bindings.validateAdmission(target)
	}
	if err != nil {
		return err
	}
	return runtimeEngine.activations.replace(
		replaceContext,
		mounted,
		target,
	)
}

// Shutdown closes admission and stops all Plugins in dependency-safe order.
func (runtimeEngine *Runtime) Shutdown(stopContext context.Context) error {
	if runtimeEngine == nil {
		return nil
	}
	runtimeEngine.operations.Lock()
	defer runtimeEngine.operations.Unlock()
	if runtimeEngine.closed {
		return nil
	}
	runtimeEngine.closed = true
	return runtimeEngine.activations.shutdown(stopContext)
}

// Status returns immutable diagnostics for one mounted Plugin.
func (runtimeEngine *Runtime) Status(pluginHandle Handle) (FiberStatus, error) {
	if runtimeEngine == nil {
		return FiberStatus{}, errors.New("plugin: status through nil Runtime")
	}
	runtimeEngine.operations.Lock()
	defer runtimeEngine.operations.Unlock()
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
	runtimeEngine.operations.Lock()
	defer runtimeEngine.operations.Unlock()
	runtimeEngine.view.RLock()
	defer runtimeEngine.view.RUnlock()
	snapshots := make([]FiberStatus, 0, runtimeEngine.mounts.length())
	for _, mounted := range runtimeEngine.mounts.all() {
		if !mounted.removed {
			snapshots = append(snapshots, statusOf(mounted))
		}
	}
	return snapshots
}

func (runtimeEngine *Runtime) lookup(pluginHandle Handle) (*pluginMount, error) {
	if pluginHandle.owner != runtimeEngine {
		return nil, errors.New("plugin: Handle belongs to another Runtime")
	}
	mounted, found := runtimeEngine.mounts.lookup(pluginHandle.id)
	if !found {
		return nil, errors.New("plugin: Handle is not mounted")
	}
	return mounted, nil
}

func fiberOf(owner Plugin) (*fiber, error) {
	if owner == nil || owner.RuntimePlugin() == nil {
		return nil, ErrPluginNotBound
	}
	running := owner.RuntimePlugin().currentFiber()
	if running == nil || running.runtime == nil || running.mount == nil {
		return nil, ErrPluginNotBound
	}
	return running, nil
}

func activeFiberOf(owner Plugin) (*fiber, error) {
	running, err := fiberOf(owner)
	if err != nil {
		return nil, err
	}
	running.runtime.view.RLock()
	active := running.state == FiberActive
	running.runtime.view.RUnlock()
	if !active {
		return nil, ErrPluginNotActive
	}
	return running, nil
}

var closedLifetime = func() context.Context {
	closedContext, cancelLifetime := context.WithCancelCause(context.Background())
	cancelLifetime(ErrPluginNotBound)
	return closedContext
}()

// Lifetime returns the current Plugin activation lifetime.
func Lifetime(owner Plugin) context.Context {
	running, err := fiberOf(owner)
	if err != nil {
		return closedLifetime
	}
	running.runtime.view.RLock()
	fiberLifetime := running.lifetime
	running.runtime.view.RUnlock()
	if fiberLifetime == nil {
		return closedLifetime
	}
	return fiberLifetime
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
