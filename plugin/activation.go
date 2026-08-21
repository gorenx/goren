package plugin

import (
	"context"
	"errors"
	"fmt"
)

// activationCoordinator owns ordering across Fibers. A Fiber still owns its
// own Apply-to-Dispose lifecycle.
type activationCoordinator struct {
	runtime   *Runtime
	mounts    *mountTree
	graph     *dependencyGraph
	nextFiber FiberID
}

func newActivationCoordinator(
	runtimeEngine *Runtime,
	mounts *mountTree,
	graph *dependencyGraph,
) *activationCoordinator {
	return &activationCoordinator{
		runtime: runtimeEngine,
		mounts:  mounts,
		graph:   graph,
	}
}

func (coordinator *activationCoordinator) attach(mounted *pluginMount) *fiber {
	coordinator.nextFiber++
	running := &fiber{
		id:      coordinator.nextFiber,
		runtime: coordinator.runtime,
		mount:   mounted,
		target:  mounted.target,
		scope:   mounted.scope,
		state:   FiberWaiting,
		calls:   newFiberCallGate(),
	}
	mounted.current = running
	return running
}

func (coordinator *activationCoordinator) newFiberCandidate(
	mounted *pluginMount,
	target pluginTarget,
) *fiber {
	coordinator.nextFiber++
	return &fiber{
		id:      coordinator.nextFiber,
		runtime: coordinator.runtime,
		mount:   mounted,
		target:  target,
		scope:   mounted.scope,
		state:   FiberWaiting,
		calls:   newFiberCallGate(),
	}
}

func (coordinator *activationCoordinator) converge(
	reconcileContext context.Context,
	phase ActivationPhase,
	mounts []*pluginMount,
) error {
	coordinator.prepareStopped(mounts)
	refreshedOptional := make(map[*pluginMount]struct{})
	for {
		progress := false
		for _, mounted := range mounts {
			running := mounted.current
			if mounted.removed || mounted.phase != phase || running == nil ||
				running.state != FiberWaiting {
				continue
			}
			resolution := coordinator.graph.resolve(
				mounted,
				mounted.target,
				mounts,
			)
			coordinator.runtime.view.Lock()
			running.missing = resolution.missingNames()
			coordinator.runtime.view.Unlock()
			if !resolution.ready() {
				continue
			}
			if err := coordinator.activate(
				reconcileContext,
				running,
				resolution,
			); err != nil {
				return err
			}
			progress = true
		}
		staleOptional := coordinator.graph.staleOptionalConsumers(mounts)
		if len(staleOptional) != 0 {
			visited := make(map[*fiber]struct{})
			var refreshErr error
			for _, running := range staleOptional {
				if _, repeated := refreshedOptional[running.mount]; repeated {
					return fmt.Errorf(
						"plugin: optional Service dependency cycle while reactivating %s",
						running.target.manifest.name,
					)
				}
				refreshedOptional[running.mount] = struct{}{}
				refreshErr = errors.Join(
					refreshErr,
					coordinator.stopFiberWithDependents(
						reconcileContext,
						running,
						visited,
					),
				)
			}
			coordinator.prepareStopped(mounts)
			if refreshErr != nil {
				return refreshErr
			}
			progress = true
		}
		if !progress {
			return nil
		}
	}
}

func (coordinator *activationCoordinator) activate(
	applyContext context.Context,
	running *fiber,
	resolution dependencyResolution,
) error {
	coordinator.runtime.view.RLock()
	preflightErr := coordinator.runtime.bindings.validate(running)
	coordinator.runtime.view.RUnlock()
	if preflightErr != nil {
		coordinator.runtime.view.Lock()
		running.state = FiberFailed
		running.lastError = preflightErr
		coordinator.runtime.view.Unlock()
		return preflightErr
	}
	applyErr := running.prepare(applyContext, resolution)
	if applyErr != nil {
		rollbackErr := running.rollback(applyContext, applyErr)
		return errors.Join(applyErr, rollbackErr)
	}
	coordinator.runtime.view.Lock()
	published, publicationErr := coordinator.runtime.bindings.publish(running)
	if publicationErr == nil {
		running.activate(published)
		coordinator.graph.addConsumer(running)
	}
	coordinator.runtime.view.Unlock()
	if publicationErr == nil {
		return nil
	}
	rollbackErr := running.rollback(applyContext, publicationErr)
	return errors.Join(publicationErr, rollbackErr)
}

func (coordinator *activationCoordinator) stopDependents(
	stopContext context.Context,
	provider *fiber,
	visited map[*fiber]struct{},
) error {
	if provider == nil {
		return nil
	}
	var stopErr error
	for _, dependent := range stopFiberOrder(
		coordinator.graph.directDependents(provider),
	) {
		stopErr = errors.Join(
			stopErr,
			coordinator.stopFiberWithDependents(
				stopContext,
				dependent,
				visited,
			),
		)
	}
	for _, childMount := range stopMountOrder(provider.mount.children) {
		if childMount.current == nil {
			continue
		}
		stopErr = errors.Join(
			stopErr,
			coordinator.stopFiberWithDependents(
				stopContext,
				childMount.current,
				visited,
			),
		)
	}
	return stopErr
}

func (coordinator *activationCoordinator) stopFiberWithDependents(
	stopContext context.Context,
	running *fiber,
	visited map[*fiber]struct{},
) error {
	if running == nil {
		return nil
	}
	if _, exists := visited[running]; exists {
		return nil
	}
	visited[running] = struct{}{}
	dependentErr := coordinator.stopDependents(
		stopContext,
		running,
		visited,
	)
	disposeErr := running.stop(stopContext)
	return errors.Join(dependentErr, disposeErr)
}

func (coordinator *activationCoordinator) rollbackMounts(
	rollbackContext context.Context,
	mounts []*pluginMount,
) error {
	roots := selectedMountRoots(mounts)
	for _, mounted := range roots {
		coordinator.mounts.markRemoved(mounted)
	}
	var rollbackErr error
	visited := make(map[*fiber]struct{})
	for mountIndex := len(mounts) - 1; mountIndex >= 0; mountIndex-- {
		rollbackErr = errors.Join(
			rollbackErr,
			coordinator.stopFiberWithDependents(
				rollbackContext,
				mounts[mountIndex].current,
				visited,
			),
		)
	}
	coordinator.mounts.deleteRoots(roots)
	coordinator.prepareStopped(coordinator.mounts.all())
	return rollbackErr
}

func selectedMountRoots(mounts []*pluginMount) []*pluginMount {
	selected := make(map[*pluginMount]struct{}, len(mounts))
	for _, mounted := range mounts {
		if mounted != nil {
			selected[mounted] = struct{}{}
		}
	}
	roots := make([]*pluginMount, 0)
	for _, mounted := range mounts {
		if mounted == nil {
			continue
		}
		if _, parentSelected := selected[mounted.parent]; parentSelected {
			continue
		}
		roots = append(roots, mounted)
	}
	return roots
}

func (coordinator *activationCoordinator) removeMount(
	removeContext context.Context,
	mounted *pluginMount,
) error {
	stopErr := coordinator.removeMountTree(removeContext, mounted)
	coordinator.prepareStopped(coordinator.mounts.all())
	return stopErr
}

func (coordinator *activationCoordinator) removeIsolatedMount(
	removeContext context.Context,
	mounted *pluginMount,
) error {
	return coordinator.removeMountTree(removeContext, mounted)
}

func (coordinator *activationCoordinator) removeMountTree(
	removeContext context.Context,
	mounted *pluginMount,
) error {
	coordinator.mounts.markRemoved(mounted)
	stopErr := coordinator.stopFiberWithDependents(
		removeContext,
		mounted.current,
		make(map[*fiber]struct{}),
	)
	coordinator.mounts.deleteTree(mounted)
	return stopErr
}

func (coordinator *activationCoordinator) shutdown(
	stopContext context.Context,
) error {
	coordinator.mounts.markAllRemoved()
	allMounts := coordinator.mounts.all()
	var shutdownErr error
	visited := make(map[*fiber]struct{})
	for mountIndex := len(allMounts) - 1; mountIndex >= 0; mountIndex-- {
		shutdownErr = errors.Join(
			shutdownErr,
			coordinator.stopFiberWithDependents(
				stopContext,
				allMounts[mountIndex].current,
				visited,
			),
		)
	}
	coordinator.mounts.clear()
	return shutdownErr
}

func stopMountOrder(mounts []*pluginMount) []*pluginMount {
	ordered := make([]*pluginMount, 0, len(mounts))
	for _, phase := range []ActivationPhase{ActivationCommit, ActivationMain} {
		for mountIndex := len(mounts) - 1; mountIndex >= 0; mountIndex-- {
			mounted := mounts[mountIndex]
			if mounted != nil && mounted.phase == phase {
				ordered = append(ordered, mounted)
			}
		}
	}
	return ordered
}

func stopFiberOrder(fibers []*fiber) []*fiber {
	ordered := make([]*fiber, 0, len(fibers))
	for _, phase := range []ActivationPhase{ActivationCommit, ActivationMain} {
		for fiberIndex := len(fibers) - 1; fiberIndex >= 0; fiberIndex-- {
			running := fibers[fiberIndex]
			if running != nil && running.mount != nil && running.mount.phase == phase {
				ordered = append(ordered, running)
			}
		}
	}
	return ordered
}

func (coordinator *activationCoordinator) prepareStopped(
	mounts []*pluginMount,
) {
	for _, mounted := range mounts {
		if mounted.removed {
			continue
		}
		if mounted.current == nil || mounted.current.state == FiberStopped {
			coordinator.attach(mounted)
		}
	}
}
