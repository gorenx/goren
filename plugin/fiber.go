package plugin

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

type fiber struct {
	id           FiberID
	mount        *mountedPlugin
	instance     Plugin
	manifest     manifestSpec
	scope        *scope
	state        FiberState
	dependencies map[reflect.Type]*serviceDependency
	missing      []string
	lifetime     context.Context
	cancel       context.CancelCauseFunc
	activation   *activation
	effects      effectStack
	services     []*serviceBinding
	events       []*eventBinding
	waterfalls   []*waterfallBinding
	lastErr      error
}

func (runtimeEngine *Runtime) newFiber(
	mounted *mountedPlugin,
	pluginInstance Plugin,
	metadata manifestSpec,
) *fiber {
	runtimeEngine.nextFiber++
	return &fiber{
		id:           runtimeEngine.nextFiber,
		mount:        mounted,
		instance:     pluginInstance,
		manifest:     metadata,
		scope:        mounted.scope,
		state:        FiberWaiting,
		dependencies: make(map[reflect.Type]*serviceDependency),
	}
}

func (runtimeEngine *Runtime) admit(
	pluginInstance Plugin,
	parentMount *mountedPlugin,
) (*mountedPlugin, error) {
	metadata, err := normalizeManifest(pluginInstance)
	if err != nil {
		return nil, err
	}
	if runtimeEngine.eventFailures == nil {
		for _, offer := range metadata.events {
			if offer.reference.policy == DeliveryBestEffort {
				return nil, fmt.Errorf(
					"plugin: Event %q requires an EventFailureReporter for best-effort delivery",
					offer.reference.name,
				)
			}
		}
	}
	selectedScope := runtimeEngine.rootScope
	if parentMount != nil {
		selectedScope = newChildScope(parentMount.scope)
	}
	for _, existingMount := range runtimeEngine.mounts {
		if existingMount.removed || existingMount.scope != selectedScope {
			continue
		}
		for _, existingOffer := range existingMount.manifest.provides {
			for _, candidateOffer := range metadata.provides {
				if existingOffer.reference.key == candidateOffer.reference.key {
					return nil, fmt.Errorf(
						"%w: %s",
						ErrServiceConflict,
						candidateOffer.reference.name,
					)
				}
			}
		}
	}
	runtimeEngine.nextHandle++
	mounted := &mountedPlugin{
		handleID: runtimeEngine.nextHandle,
		parent:   parentMount,
		scope:    selectedScope,
		instance: pluginInstance,
		manifest: metadata,
	}
	mounted.current = runtimeEngine.newFiber(
		mounted,
		pluginInstance,
		metadata,
	)
	if parentMount != nil {
		parentMount.children = append(parentMount.children, mounted)
	}
	runtimeEngine.mounts = append(runtimeEngine.mounts, mounted)
	runtimeEngine.byHandle[mounted.handleID] = mounted
	return mounted, nil
}

func (runtimeEngine *Runtime) reconcile(reconcileContext context.Context) error {
	runtimeEngine.prepareStopped()
	for {
		progress := false
		for _, mounted := range runtimeEngine.mounts {
			if mounted.removed || mounted.current == nil ||
				mounted.current.state != FiberWaiting {
				continue
			}
			dependencies, missing, blocked := runtimeEngine.resolveDependencies(mounted)
			runtimeEngine.state.Lock()
			mounted.current.missing = missing
			runtimeEngine.state.Unlock()
			if len(missing) != 0 || blocked {
				continue
			}
			if err := runtimeEngine.prepareFiber(
				reconcileContext,
				mounted.current,
				dependencies,
			); err != nil {
				return err
			}
			runtimeEngine.state.Lock()
			publicationErr := runtimeEngine.publishContributionsLocked(mounted.current)
			if publicationErr == nil {
				mounted.current.state = FiberActive
			}
			runtimeEngine.state.Unlock()
			if publicationErr != nil {
				rollbackErr := runtimeEngine.rollbackPrepared(
					reconcileContext,
					mounted.current,
					publicationErr,
				)
				return errors.Join(publicationErr, rollbackErr)
			}
			progress = true
		}
		if !progress {
			return nil
		}
	}
}

func (runtimeEngine *Runtime) resolveDependencies(
	mounted *mountedPlugin,
) (map[reflect.Type]*serviceDependency, []string, bool) {
	dependencies := make(map[reflect.Type]*serviceDependency)
	missing := make([]string, 0)
	blocked := false

	runtimeEngine.state.RLock()
	defer runtimeEngine.state.RUnlock()
	if mounted.parent != nil {
		if mounted.parent.removed || mounted.parent.current == nil ||
			mounted.parent.current.state != FiberActive {
			blocked = true
		}
	}
	for _, reference := range mounted.manifest.requires {
		binding, available := runtimeEngine.services.resolve(
			reference,
			mounted.scope,
		)
		if available {
			dependencies[reference.key] = &serviceDependency{
				reference: reference,
				binding:   binding,
			}
			continue
		}
		if runtimeEngine.declaredProvider(reference, mounted.scope) != nil {
			blocked = true
			continue
		}
		missing = append(missing, reference.name)
	}
	for _, reference := range mounted.manifest.optional {
		binding, available := runtimeEngine.services.resolve(
			reference,
			mounted.scope,
		)
		if !available {
			continue
		}
		dependencies[reference.key] = &serviceDependency{
			reference: reference,
			binding:   binding,
			optional:  true,
		}
	}
	sort.Strings(missing)
	return dependencies, missing, blocked
}

// Runtime.state must be read-locked by the caller.
func (runtimeEngine *Runtime) declaredProvider(
	reference serviceRef,
	sourceScope *scope,
) *mountedPlugin {
	for selectedScope := sourceScope; selectedScope != nil; selectedScope = selectedScope.parent {
		for _, mounted := range runtimeEngine.mounts {
			if mounted.removed || mounted.scope != selectedScope {
				continue
			}
			for _, offer := range mounted.manifest.provides {
				if offer.reference.key == reference.key {
					return mounted
				}
			}
		}
	}
	return nil
}

func (runtimeEngine *Runtime) prepareFiber(
	applyContext context.Context,
	ownerFiber *fiber,
	dependencies map[reflect.Type]*serviceDependency,
) error {
	fiberLifetime, cancelLifetime := context.WithCancelCause(context.Background())
	selectedActivation := &activation{
		runtime:  runtimeEngine,
		fiber:    ownerFiber,
		lifetime: fiberLifetime,
	}
	if err := ownerFiber.instance.RuntimePlugin().attach(selectedActivation); err != nil {
		runtimeEngine.state.Lock()
		ownerFiber.state = FiberFailed
		ownerFiber.lastErr = err
		runtimeEngine.state.Unlock()
		cancelLifetime(err)
		return err
	}
	ownerFiber.lifetime = fiberLifetime
	ownerFiber.cancel = cancelLifetime
	ownerFiber.activation = selectedActivation
	ownerFiber.dependencies = dependencies
	ownerFiber.missing = nil
	ownerFiber.effects.add(
		"plugin:"+ownerFiber.manifest.name,
		ownerFiber.instance.Dispose,
	)
	runtimeEngine.state.Lock()
	ownerFiber.state = FiberStarting
	runtimeEngine.state.Unlock()

	applyErr := ownerFiber.instance.Apply(applyContext)
	if applyErr == nil {
		return nil
	}
	rollbackErr := runtimeEngine.rollbackPrepared(
		applyContext,
		ownerFiber,
		applyErr,
	)
	return errors.Join(applyErr, rollbackErr)
}

func (runtimeEngine *Runtime) rollbackPrepared(
	rollbackContext context.Context,
	ownerFiber *fiber,
	failure error,
) error {
	runtimeEngine.state.Lock()
	ownerFiber.state = FiberRollingBack
	runtimeEngine.withdrawContributionsLocked(ownerFiber)
	runtimeEngine.state.Unlock()
	if ownerFiber.cancel != nil {
		ownerFiber.cancel(failure)
	}
	cleanupErr := ownerFiber.effects.release(rollbackContext)
	ownerFiber.instance.RuntimePlugin().detach(ownerFiber.activation)
	runtimeEngine.state.Lock()
	ownerFiber.state = FiberFailed
	ownerFiber.lastErr = errors.Join(failure, cleanupErr)
	runtimeEngine.state.Unlock()
	return cleanupErr
}

// Runtime.state must be write-locked by the caller.
func (runtimeEngine *Runtime) publishContributionsLocked(ownerFiber *fiber) error {
	for _, offer := range ownerFiber.manifest.provides {
		bindingKey := serviceBindingKey{
			scope:       ownerFiber.scope,
			serviceType: offer.reference.key,
		}
		existingBinding := runtimeEngine.services.bindings[bindingKey]
		if existingBinding != nil && existingBinding.owner != ownerFiber {
			return fmt.Errorf(
				"%w: %s",
				ErrServiceConflict,
				offer.reference.name,
			)
		}
	}
	for _, offer := range ownerFiber.manifest.events {
		for _, existingBinding := range runtimeEngine.events.bindings[offer.reference.key] {
			if existingBinding.reference.name != offer.reference.name ||
				existingBinding.reference.policy != offer.reference.policy {
				return fmt.Errorf(
					"plugin: Event type %q has inconsistent metadata",
					namedTypeName(offer.reference.key),
				)
			}
		}
	}

	for _, offer := range ownerFiber.manifest.provides {
		selectedBinding := &serviceBinding{
			reference:  offer.reference,
			capability: offer.capability,
			owner:      ownerFiber,
			scope:      ownerFiber.scope,
		}
		bindingKey := serviceBindingKey{
			scope:       ownerFiber.scope,
			serviceType: offer.reference.key,
		}
		runtimeEngine.services.bindings[bindingKey] = selectedBinding
		ownerFiber.services = append(ownerFiber.services, selectedBinding)
		ownerFiber.effects.add(
			"service:"+offer.reference.name,
			func(context.Context) error {
				runtimeEngine.state.Lock()
				if runtimeEngine.services.bindings[bindingKey] == selectedBinding {
					delete(runtimeEngine.services.bindings, bindingKey)
				}
				runtimeEngine.state.Unlock()
				return nil
			},
		)
	}
	for _, offer := range ownerFiber.manifest.events {
		runtimeEngine.nextOrdinal++
		selectedBinding := &eventBinding{
			reference: offer.reference,
			observer:  offer.observer,
			owner:     ownerFiber,
			scope:     ownerFiber.scope,
			ordinal:   runtimeEngine.nextOrdinal,
		}
		runtimeEngine.events.add(selectedBinding)
		ownerFiber.events = append(ownerFiber.events, selectedBinding)
		ownerFiber.effects.add(
			"event:"+offer.reference.name,
			func(context.Context) error {
				runtimeEngine.state.Lock()
				runtimeEngine.events.remove(selectedBinding)
				runtimeEngine.state.Unlock()
				return nil
			},
		)
	}
	for _, offer := range ownerFiber.manifest.waterfalls {
		runtimeEngine.nextOrdinal++
		selectedBinding := &waterfallBinding{
			reference: offer.reference,
			invoker:   offer.invoker,
			owner:     ownerFiber,
			scope:     ownerFiber.scope,
			ordinal:   runtimeEngine.nextOrdinal,
		}
		runtimeEngine.waterfalls.add(selectedBinding)
		ownerFiber.waterfalls = append(ownerFiber.waterfalls, selectedBinding)
		ownerFiber.effects.add(
			"waterfall:"+offer.reference.name,
			func(context.Context) error {
				runtimeEngine.state.Lock()
				runtimeEngine.waterfalls.remove(selectedBinding)
				runtimeEngine.state.Unlock()
				return nil
			},
		)
	}
	return nil
}

// Runtime.state must be write-locked by the caller.
func (runtimeEngine *Runtime) withdrawContributionsLocked(ownerFiber *fiber) {
	if ownerFiber == nil {
		return
	}
	for _, selectedBinding := range ownerFiber.services {
		bindingKey := serviceBindingKey{
			scope:       selectedBinding.scope,
			serviceType: selectedBinding.reference.key,
		}
		if runtimeEngine.services.bindings[bindingKey] == selectedBinding {
			delete(runtimeEngine.services.bindings, bindingKey)
		}
	}
	for _, selectedBinding := range ownerFiber.events {
		runtimeEngine.events.remove(selectedBinding)
	}
	for _, selectedBinding := range ownerFiber.waterfalls {
		runtimeEngine.waterfalls.remove(selectedBinding)
	}
}

// Runtime.state must be write-locked by the caller.
func (runtimeEngine *Runtime) publishExistingContributionsLocked(ownerFiber *fiber) {
	if ownerFiber == nil {
		return
	}
	for _, selectedBinding := range ownerFiber.services {
		runtimeEngine.services.bindings[serviceBindingKey{
			scope:       selectedBinding.scope,
			serviceType: selectedBinding.reference.key,
		}] = selectedBinding
	}
	for _, selectedBinding := range ownerFiber.events {
		runtimeEngine.events.add(selectedBinding)
	}
	for _, selectedBinding := range ownerFiber.waterfalls {
		runtimeEngine.waterfalls.add(selectedBinding)
	}
}

func (runtimeEngine *Runtime) stopDependents(
	stopContext context.Context,
	providerFiber *fiber,
	visited map[*fiber]struct{},
) error {
	if providerFiber == nil {
		return nil
	}
	var stopErr error
	for _, dependentFiber := range runtimeEngine.directDependents(providerFiber) {
		stopErr = errors.Join(
			stopErr,
			runtimeEngine.stopFiberWithDependents(
				stopContext,
				dependentFiber,
				visited,
			),
		)
	}
	for _, childMount := range providerFiber.mount.children {
		if childMount.removed || childMount.current == nil {
			continue
		}
		stopErr = errors.Join(
			stopErr,
			runtimeEngine.stopFiberWithDependents(
				stopContext,
				childMount.current,
				visited,
			),
		)
	}
	return stopErr
}

func (runtimeEngine *Runtime) stopFiberWithDependents(
	stopContext context.Context,
	ownerFiber *fiber,
	visited map[*fiber]struct{},
) error {
	if ownerFiber == nil {
		return nil
	}
	if _, exists := visited[ownerFiber]; exists {
		return nil
	}
	visited[ownerFiber] = struct{}{}
	dependentErr := runtimeEngine.stopDependents(
		stopContext,
		ownerFiber,
		visited,
	)
	disposeErr := runtimeEngine.disposeFiber(stopContext, ownerFiber)
	return errors.Join(dependentErr, disposeErr)
}

func (runtimeEngine *Runtime) directDependents(providerFiber *fiber) []*fiber {
	dependents := make([]*fiber, 0)
	runtimeEngine.state.RLock()
	for _, mounted := range runtimeEngine.mounts {
		candidateFiber := mounted.current
		if candidateFiber == nil ||
			candidateFiber.state != FiberActive || candidateFiber == providerFiber {
			continue
		}
		for _, dependency := range candidateFiber.dependencies {
			if dependency.binding == nil || dependency.binding.owner != providerFiber {
				continue
			}
			dependents = append(dependents, candidateFiber)
			break
		}
	}
	runtimeEngine.state.RUnlock()
	return dependents
}

func (runtimeEngine *Runtime) disposeFiber(
	disposeContext context.Context,
	ownerFiber *fiber,
) error {
	if ownerFiber == nil {
		return nil
	}
	runtimeEngine.state.Lock()
	switch ownerFiber.state {
	case FiberStopped:
		runtimeEngine.state.Unlock()
		return nil
	case FiberWaiting:
		ownerFiber.state = FiberStopped
		runtimeEngine.state.Unlock()
		return nil
	case FiberFailed:
		ownerFiber.state = FiberStopped
		runtimeEngine.state.Unlock()
		return ownerFiber.lastErr
	default:
		ownerFiber.state = FiberStopping
	}
	runtimeEngine.state.Unlock()
	if ownerFiber.cancel != nil {
		ownerFiber.cancel(ErrPluginNotActive)
	}
	disposeErr := ownerFiber.effects.release(disposeContext)
	ownerFiber.instance.RuntimePlugin().detach(ownerFiber.activation)
	runtimeEngine.state.Lock()
	ownerFiber.state = FiberStopped
	ownerFiber.lastErr = disposeErr
	runtimeEngine.state.Unlock()
	return disposeErr
}

func (runtimeEngine *Runtime) prepareStopped() {
	for _, mounted := range runtimeEngine.mounts {
		if mounted.removed {
			continue
		}
		if mounted.current == nil || mounted.current.state == FiberStopped {
			mounted.current = runtimeEngine.newFiber(
				mounted,
				mounted.instance,
				mounted.manifest,
			)
		}
	}
}

func (runtimeEngine *Runtime) readinessError(mounts []*mountedPlugin) error {
	var readinessErr error
	for _, mounted := range mounts {
		if mounted.removed || mounted.current == nil {
			continue
		}
		ownerFiber := mounted.current
		if ownerFiber.state == FiberActive {
			continue
		}
		switch ownerFiber.state {
		case FiberWaiting:
			reason := "dependency cycle"
			if len(ownerFiber.missing) != 0 {
				reason = fmt.Sprintf(
					"missing required Services: %v",
					ownerFiber.missing,
				)
			}
			readinessErr = errors.Join(
				readinessErr,
				fmt.Errorf(
					"plugin: start %s: %s",
					ownerFiber.manifest.name,
					reason,
				),
			)
		case FiberFailed:
			readinessErr = errors.Join(readinessErr, ownerFiber.lastErr)
		default:
			readinessErr = errors.Join(
				readinessErr,
				fmt.Errorf(
					"plugin: start %s stopped in state %s",
					ownerFiber.manifest.name,
					ownerFiber.state,
				),
			)
		}
	}
	return readinessErr
}

func (runtimeEngine *Runtime) rollbackFrom(
	rollbackContext context.Context,
	startIndex int,
) error {
	if startIndex < 0 || startIndex > len(runtimeEngine.mounts) {
		return nil
	}
	rollbackMounts := append(
		[]*mountedPlugin(nil),
		runtimeEngine.mounts[startIndex:]...,
	)
	for _, mounted := range rollbackMounts {
		runtimeEngine.markRemovedTree(mounted)
	}
	var rollbackErr error
	visited := make(map[*fiber]struct{})
	for mountIndex := len(rollbackMounts) - 1; mountIndex >= 0; mountIndex-- {
		rollbackErr = errors.Join(
			rollbackErr,
			runtimeEngine.stopFiberWithDependents(
				rollbackContext,
				rollbackMounts[mountIndex].current,
				visited,
			),
		)
	}
	for _, mounted := range rollbackMounts {
		runtimeEngine.deleteMountTree(mounted)
	}
	runtimeEngine.prepareStopped()
	return rollbackErr
}

func (runtimeEngine *Runtime) removeMounted(
	removeContext context.Context,
	mounted *mountedPlugin,
) error {
	runtimeEngine.markRemovedTree(mounted)
	stopErr := runtimeEngine.stopFiberWithDependents(
		removeContext,
		mounted.current,
		make(map[*fiber]struct{}),
	)
	runtimeEngine.deleteMountTree(mounted)
	runtimeEngine.prepareStopped()
	return stopErr
}

func (runtimeEngine *Runtime) markRemovedTree(mounted *mountedPlugin) {
	if mounted == nil || mounted.removed {
		return
	}
	mounted.removed = true
	for _, childMount := range mounted.children {
		runtimeEngine.markRemovedTree(childMount)
	}
}

func (runtimeEngine *Runtime) deleteMountTree(mounted *mountedPlugin) {
	if mounted == nil {
		return
	}
	removeSet := make(map[*mountedPlugin]struct{})
	var collect func(*mountedPlugin)
	collect = func(selectedMount *mountedPlugin) {
		if selectedMount == nil {
			return
		}
		removeSet[selectedMount] = struct{}{}
		for _, childMount := range selectedMount.children {
			collect(childMount)
		}
	}
	collect(mounted)
	for selectedMount := range removeSet {
		delete(runtimeEngine.byHandle, selectedMount.handleID)
	}
	filtered := runtimeEngine.mounts[:0]
	for _, candidateMount := range runtimeEngine.mounts {
		if _, removed := removeSet[candidateMount]; !removed {
			filtered = append(filtered, candidateMount)
		}
	}
	runtimeEngine.mounts = filtered
	if mounted.parent != nil {
		children := mounted.parent.children[:0]
		for _, childMount := range mounted.parent.children {
			if _, removed := removeSet[childMount]; !removed {
				children = append(children, childMount)
			}
		}
		mounted.parent.children = children
	}
}
