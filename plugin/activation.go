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
		id:           coordinator.nextFiber,
		runtime:      coordinator.runtime,
		mount:        mounted,
		target:       mounted.target,
		scope:        mounted.scope,
		state:        FiberWaiting,
		dependencies: make(dependencySnapshot),
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
		id:           coordinator.nextFiber,
		runtime:      coordinator.runtime,
		mount:        mounted,
		target:       target,
		scope:        mounted.scope,
		state:        FiberWaiting,
		dependencies: make(dependencySnapshot),
	}
}

func (coordinator *activationCoordinator) converge(
	reconcileContext context.Context,
) error {
	coordinator.prepareStopped()
	refreshedOptional := make(map[*pluginMount]struct{})
	for {
		progress := false
		for _, mounted := range coordinator.mounts.all() {
			running := mounted.current
			if mounted.removed || running == nil || running.state != FiberWaiting {
				continue
			}
			resolution := coordinator.graph.resolve(mounted, mounted.target)
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
		staleOptional := coordinator.graph.staleOptionalConsumers()
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
			coordinator.prepareStopped()
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
	for _, dependent := range coordinator.graph.directDependents(provider) {
		stopErr = errors.Join(
			stopErr,
			coordinator.stopFiberWithDependents(
				stopContext,
				dependent,
				visited,
			),
		)
	}
	for _, childMount := range provider.mount.children {
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
	for _, mounted := range mounts {
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
	for _, mounted := range mounts {
		coordinator.mounts.deleteTree(mounted)
	}
	coordinator.prepareStopped()
	return rollbackErr
}

func (coordinator *activationCoordinator) removeMount(
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
	coordinator.prepareStopped()
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

func (coordinator *activationCoordinator) replace(
	replaceContext context.Context,
	mounted *pluginMount,
	target pluginTarget,
) error {
	if !sameManifestContract(mounted.target.manifest, target.manifest) {
		return errors.New("plugin: replacement changes the mounted Plugin contract")
	}
	resolution := coordinator.graph.resolve(mounted, target)
	if !resolution.ready() {
		return fmt.Errorf(
			"plugin: replacement dependencies are unavailable: %v",
			resolution.missingNames(),
		)
	}
	candidate := coordinator.newFiberCandidate(mounted, target)
	applyErr := candidate.prepare(replaceContext, resolution)
	if applyErr != nil {
		rollbackErr := candidate.rollback(replaceContext, applyErr)
		return errors.Join(applyErr, rollbackErr)
	}

	previous := mounted.current
	stopErr := coordinator.stopDependents(
		replaceContext,
		previous,
		make(map[*fiber]struct{}),
	)
	coordinator.runtime.view.Lock()
	coordinator.runtime.bindings.withdraw(previous.bindings)
	published, publicationErr := coordinator.runtime.bindings.publish(candidate)
	if publicationErr != nil {
		coordinator.runtime.bindings.restore(previous.bindings)
		coordinator.runtime.view.Unlock()
		rollbackErr := candidate.rollback(replaceContext, publicationErr)
		coordinator.prepareStopped()
		settleErr := coordinator.converge(replaceContext)
		return errors.Join(stopErr, publicationErr, rollbackErr, settleErr)
	}
	previous.state = FiberStopping
	candidate.activate(published)
	mounted.target = target
	mounted.current = candidate
	coordinator.runtime.view.Unlock()

	disposeErr := previous.stop(replaceContext)
	coordinator.prepareStopped()
	settleErr := coordinator.converge(replaceContext)
	return errors.Join(stopErr, disposeErr, settleErr)
}

func (coordinator *activationCoordinator) prepareStopped() {
	for _, mounted := range coordinator.mounts.all() {
		if mounted.removed {
			continue
		}
		if mounted.current == nil || mounted.current.state == FiberStopped {
			coordinator.attach(mounted)
		}
	}
}
