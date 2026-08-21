package plugin

import (
	"context"
	"errors"
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
	operations chan struct{}
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
		operations: make(chan struct{}, 1),
		mounts:     newMountTree(),
		bindings:   newRuntimeBindings(settings.EventFailures),
	}
	runtimeEngine.operations <- struct{}{}
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
	if len(instances) == 0 {
		return nil, errors.New("plugin: Start requires at least one Plugin")
	}
	forest, err := buildPluginForest(instances)
	if err != nil {
		return nil, err
	}
	if err = runtimeEngine.beginOperation(startContext); err != nil {
		return nil, err
	}
	defer runtimeEngine.endOperation()
	if runtimeEngine.closed {
		return nil, errors.New("plugin: Runtime is shut down")
	}
	if runtimeEngine.started {
		return nil, errors.New("plugin: Runtime has already started")
	}
	handles := make([]Handle, 0, len(forest.roots))
	rootMounts, startedMounts, admissionErr := runtimeEngine.mounts.admitDeclarations(
		forest.roots,
		nil,
		mountAtRootScope,
	)
	if admissionErr != nil {
		return nil, admissionErr
	}
	for _, mounted := range rootMounts {
		handles = append(handles, handleOf(runtimeEngine, mounted))
	}
	runtimeEngine.view.RLock()
	admissionErr = runtimeEngine.bindings.validateMountAdmission(startedMounts)
	runtimeEngine.view.RUnlock()
	if admissionErr != nil {
		runtimeEngine.discardMountRoots(rootMounts)
		return nil, admissionErr
	}
	for _, mounted := range startedMounts {
		runtimeEngine.activations.attach(mounted)
	}
	if startErr := runtimeEngine.activateMountBatch(
		startContext,
		startedMounts,
		startedMounts,
	); startErr != nil {
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
	forest, err := buildPluginForest([]Plugin{pluginInstance})
	if err != nil {
		return Handle{}, err
	}
	if err = runtimeEngine.beginOperation(mountContext); err != nil {
		return Handle{}, err
	}
	defer runtimeEngine.endOperation()
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
	roots, admitted, err := runtimeEngine.mounts.admitDeclarations(
		forest.roots,
		parentMount,
		placement,
	)
	if err != nil {
		return Handle{}, err
	}
	mounted := roots[0]
	runtimeEngine.view.RLock()
	admissionErr := runtimeEngine.bindings.validateMountAdmission(admitted)
	runtimeEngine.view.RUnlock()
	if admissionErr != nil {
		runtimeEngine.mounts.deleteTree(mounted)
		return Handle{}, admissionErr
	}
	for _, selectedMount := range admitted {
		runtimeEngine.activations.attach(selectedMount)
	}
	// A new child Scope has no existing inhabitants, and its Services are not
	// visible to ancestors or siblings. Its activation can therefore converge
	// against the admitted subtree without scanning unrelated Runtime topology.
	isolated := placement == mountInChildScope
	topology := admitted
	if !isolated {
		topology = runtimeEngine.mounts.all()
	}
	if mountErr := runtimeEngine.activateMountBatch(
		mountContext,
		admitted,
		topology,
	); mountErr != nil {
		var rollbackErr error
		if isolated {
			rollbackErr = runtimeEngine.activations.removeIsolatedMount(
				mountContext,
				mounted,
			)
		} else {
			rollbackErr = runtimeEngine.activations.removeMount(
				mountContext,
				mounted,
			)
		}
		var reconcileErr error
		if !isolated {
			reconcileErr = runtimeEngine.reconcile(mountContext)
		}
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
	if err := runtimeEngine.beginOperation(stopContext); err != nil {
		return err
	}
	defer runtimeEngine.endOperation()
	mounted, err := runtimeEngine.lookup(pluginHandle)
	if err != nil {
		return err
	}
	stopErr := runtimeEngine.activations.removeMount(
		stopContext,
		mounted,
	)
	settleErr := runtimeEngine.reconcile(stopContext)
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
	if err := runtimeEngine.beginOperation(stopContext); err != nil {
		return err
	}
	defer runtimeEngine.endOperation()
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
	// Descendant Services cannot be consumed outside their child Scope, so an
	// owned child-Scope removal cannot leave an external Fiber to reconcile.
	isolated := childMount.placement == mountInChildScope
	var stopErr error
	if isolated {
		stopErr = runtimeEngine.activations.removeIsolatedMount(
			stopContext,
			childMount,
		)
	} else {
		stopErr = runtimeEngine.activations.removeMount(
			stopContext,
			childMount,
		)
	}
	var settleErr error
	if !isolated {
		settleErr = runtimeEngine.reconcile(stopContext)
	}
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
	forest, err := buildPluginForest([]Plugin{candidate})
	if err != nil {
		return err
	}
	if err = runtimeEngine.beginOperation(replaceContext); err != nil {
		return err
	}
	defer runtimeEngine.endOperation()
	mounted, err := runtimeEngine.lookup(pluginHandle)
	if err != nil {
		return err
	}
	declaration := forest.roots[0]
	replacement, err := newSubtreeReplacement(
		runtimeEngine,
		mounted,
		declaration,
	)
	if err != nil {
		return err
	}
	return replacement.execute(replaceContext)
}

// Shutdown closes admission and stops all Plugins in dependency-safe order.
func (runtimeEngine *Runtime) Shutdown(stopContext context.Context) error {
	if runtimeEngine == nil {
		return nil
	}
	if err := runtimeEngine.beginOperation(stopContext); err != nil {
		return err
	}
	defer runtimeEngine.endOperation()
	if runtimeEngine.closed {
		return nil
	}
	runtimeEngine.closed = true
	return runtimeEngine.activations.shutdown(stopContext)
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
