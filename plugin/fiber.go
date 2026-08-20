package plugin

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// pluginDeclaration is the stable Runtime identity returned as a Handle. Its
// current Fiber may change after dependency loss or replacement.
type pluginDeclaration struct {
	id            uint64
	instance      Plugin
	manifest      manifestSpec
	current       *fiber
	parent        *pluginDeclaration
	parentFiberID FiberID
	parentScope   *Scope
	children      map[uint64]*pluginDeclaration
	state         FiberState
	lastFiberID   FiberID
	missing       []string
	lastErr       error
	removed       bool
	ownerEffect   *fiberEffect
}

type resolvedServiceDependency struct {
	reference serviceRef
	binding   serviceBinding
	provider  *fiber
	optional  bool
}

// fiber is one concrete Plugin activation attempt. It owns the lifetime,
// Context, root Scope, private effect stack, and resolved Service dependency
// snapshot for that attempt.
type fiber struct {
	id            FiberID
	declaration   *pluginDeclaration
	instance      Plugin
	manifest      manifestSpec
	state         FiberState
	rootScope     *Scope
	pluginContext *Context
	lifetime      context.Context
	cancel        context.CancelCauseFunc
	dependencies  map[*serviceToken]resolvedServiceDependency
	effects       *fiberEffectStack
	lastErr       error
	order         uint64
}

func (ownerFiber *fiber) disposePlugin(disposeContext context.Context) error {
	if ownerFiber == nil || ownerFiber.instance == nil {
		return nil
	}
	return invokePluginDispose(disposeContext, ownerFiber.instance)
}

// fiberSupervisor owns declarations, dependency settlement, activation,
// replacement, and parent-child stop ordering. Runtime.operations serializes
// its mutations; Runtime.state protects Registry visibility and Fiber state.
type fiberSupervisor struct {
	runtime      *Runtime
	nextHandleID uint64
	nextFiberID  FiberID
	nextOrder    uint64
	declarations []*pluginDeclaration
	byHandle     map[uint64]*pluginDeclaration
	dependents   map[FiberID]map[FiberID]*fiber
	closed       bool
}

func newFiberSupervisor(runtimeEngine *Runtime) *fiberSupervisor {
	return &fiberSupervisor{
		runtime:    runtimeEngine,
		byHandle:   make(map[uint64]*pluginDeclaration),
		dependents: make(map[FiberID]map[FiberID]*fiber),
	}
}

func (supervisor *fiberSupervisor) newFiber(
	declaration *pluginDeclaration,
	instance Plugin,
	metadata manifestSpec,
) *fiber {
	supervisor.nextFiberID++
	supervisor.nextOrder++
	parentLifetime := context.Background()
	if declaration != nil && declaration.parentScope != nil &&
		declaration.parentScope.ownerFiber != nil {
		parentLifetime = declaration.parentScope.ownerFiber.lifetime
	}
	fiberLifetime, cancelLifetime := context.WithCancelCause(parentLifetime)
	return &fiber{
		id:           supervisor.nextFiberID,
		declaration:  declaration,
		instance:     instance,
		manifest:     metadata,
		state:        FiberWaiting,
		lifetime:     fiberLifetime,
		cancel:       cancelLifetime,
		dependencies: make(map[*serviceToken]resolvedServiceDependency),
		effects:      newFiberEffectStack(),
		order:        supervisor.nextOrder,
	}
}

func (supervisor *fiberSupervisor) load(
	loadContext context.Context,
	parentFiber *fiber,
	parentScope *Scope,
	instance Plugin,
) (Handle, error) {
	supervisor.runtime.operations.Lock()
	defer supervisor.runtime.operations.Unlock()
	return supervisor.loadLocked(loadContext, parentFiber, parentScope, instance)
}

func (supervisor *fiberSupervisor) loadLocked(
	loadContext context.Context,
	parentFiber *fiber,
	parentScope *Scope,
	instance Plugin,
) (Handle, error) {
	if supervisor.closed {
		return Handle{}, errors.New("plugin: Runtime is shut down")
	}
	if instance == nil {
		return Handle{}, errors.New("plugin: cannot load nil Plugin")
	}
	if parentFiber != nil {
		if !supervisor.parentAcceptsMount(parentFiber, parentScope, FiberActive) {
			return Handle{}, ErrPluginNotActive
		}
	}
	declaration, pluginHandle, err := supervisor.admitPlugin(
		parentFiber,
		parentScope,
		instance,
	)
	if err != nil {
		return Handle{}, err
	}
	if parentFiber != nil {
		parentFiber.effects.entries = append(
			parentFiber.effects.entries,
			supervisor.ownMountedPlugin(parentFiber, parentScope, declaration),
		)
	}
	reconcileErr := supervisor.reconcile(loadContext)
	if declaration.state == FiberFailed {
		return pluginHandle, errors.Join(declaration.lastErr, reconcileErr)
	}
	return pluginHandle, reconcileErr
}

func (supervisor *fiberSupervisor) mountDuringApply(
	mountContext context.Context,
	parentContext *Context,
	instance Plugin,
) (Handle, error) {
	transaction := parentContext.transaction
	if transaction == nil || transaction.state != mountOpen ||
		transaction.fiber != parentContext.ownerFiber ||
		!supervisor.parentAcceptsMount(
			parentContext.ownerFiber,
			parentContext.scope,
			FiberStarting,
		) {
		if transaction != nil && transaction.state == mountOpen {
			return Handle{}, transaction.recordFailure(ErrPluginNotActive)
		}
		return Handle{}, ErrPluginNotActive
	}
	if supervisor.closed {
		return Handle{}, transaction.recordFailure(errors.New("plugin: Runtime is shut down"))
	}
	if instance == nil {
		return Handle{}, transaction.recordFailure(errors.New("plugin: cannot mount nil Plugin"))
	}
	declaration, pluginHandle, err := supervisor.admitPlugin(
		parentContext.ownerFiber,
		parentContext.scope,
		instance,
	)
	if err != nil {
		return Handle{}, transaction.recordFailure(err)
	}
	transaction.effects = append(
		transaction.effects,
		supervisor.ownMountedPlugin(
			parentContext.ownerFiber,
			parentContext.scope,
			declaration,
		),
	)

	missing := supervisor.missingRequiredServices(declaration)
	declaration.missing = missing
	if len(missing) != 0 {
		return pluginHandle, nil
	}
	activationErr := supervisor.activate(mountContext, declaration)
	if activationErr != nil {
		return pluginHandle, transaction.recordFailure(activationErr)
	}
	return pluginHandle, nil
}

func (supervisor *fiberSupervisor) admitPlugin(
	parentFiber *fiber,
	parentScope *Scope,
	instance Plugin,
) (*pluginDeclaration, Handle, error) {
	metadata, err := pluginManifest(instance)
	if err != nil {
		return nil, Handle{}, err
	}
	supervisor.runtime.state.Lock()
	err = supervisor.runtime.services.admitManifest(metadata)
	supervisor.runtime.state.Unlock()
	if err != nil {
		return nil, Handle{}, err
	}

	supervisor.nextHandleID++
	declaration := &pluginDeclaration{
		id:          supervisor.nextHandleID,
		instance:    instance,
		manifest:    metadata,
		parentScope: parentScope,
		children:    make(map[uint64]*pluginDeclaration),
		state:       FiberWaiting,
	}
	if parentFiber != nil {
		declaration.parent = parentFiber.declaration
		declaration.parentFiberID = parentFiber.id
		declaration.parent.children[declaration.id] = declaration
	}
	supervisor.declarations = append(supervisor.declarations, declaration)
	supervisor.byHandle[declaration.id] = declaration
	pluginHandle := Handle{
		owner: supervisor.runtime,
		id:    declaration.id,
	}
	return declaration, pluginHandle, nil
}

func (supervisor *fiberSupervisor) parentAcceptsMount(
	parentFiber *fiber,
	parentScope *Scope,
	expectedState FiberState,
) bool {
	if parentFiber == nil || parentScope == nil || parentScope.isClosed() ||
		parentScope.ownerFiber != parentFiber || parentFiber.declaration == nil {
		return false
	}
	supervisor.runtime.state.RLock()
	currentState := parentFiber.state
	supervisor.runtime.state.RUnlock()
	if currentState != expectedState {
		return false
	}
	return expectedState == FiberStarting ||
		parentFiber.declaration.current == parentFiber
}

func (supervisor *fiberSupervisor) ownMountedPlugin(
	parentFiber *fiber,
	parentScope *Scope,
	declaration *pluginDeclaration,
) *fiberEffect {
	ownership := &fiberEffect{
		runtime: supervisor.runtime,
		fiber:   parentFiber,
		scope:   parentScope,
		label:   "plugin:" + declaration.manifest.Name,
		state:   fiberEffectActive,
	}
	ownership.release = func(releaseContext context.Context) error {
		return supervisor.disposeDeclaration(releaseContext, declaration)
	}
	declaration.ownerEffect = ownership
	return ownership
}

func (supervisor *fiberSupervisor) reconcile(
	settlementContext context.Context,
) error {
	var settlementErr error
	for {
		changed := false
		for _, declaration := range supervisor.declarations {
			if declaration.removed || declaration.state != FiberActive || declaration.current == nil {
				continue
			}
			if !supervisor.dependenciesChanged(declaration.current) {
				continue
			}
			stopErr := supervisor.stopForDependencyChange(settlementContext, declaration.current)
			settlementErr = errors.Join(settlementErr, stopErr)
			changed = true
			break
		}
		if changed {
			continue
		}

		for _, declaration := range supervisor.declarations {
			if declaration.removed || declaration.state != FiberWaiting ||
				!supervisor.parentReady(declaration) {
				continue
			}
			missing := supervisor.missingRequiredServices(declaration)
			declaration.missing = missing
			if len(missing) != 0 {
				continue
			}
			activationErr := supervisor.activate(settlementContext, declaration)
			settlementErr = errors.Join(settlementErr, activationErr)
			changed = true
			break
		}
		if !changed {
			return settlementErr
		}
	}
}

func (supervisor *fiberSupervisor) activate(
	applyContext context.Context,
	declaration *pluginDeclaration,
) error {
	ownerFiber := supervisor.newFiber(declaration, declaration.instance, declaration.manifest)
	ownerFiber.rootScope = newFiberRootScope(
		supervisor.runtime,
		ownerFiber,
		declaration.parentScope,
	)
	ownerFiber.rootScope.label = declaration.manifest.Name
	missing := supervisor.resolveDependencies(ownerFiber)
	if len(missing) != 0 {
		ownerFiber.cancel(ErrServiceUnavailable)
		ownerFiber.rootScope.closeTree()
		declaration.missing = missing
		return nil
	}

	transaction := newMountTransaction(supervisor.runtime, ownerFiber, applyContext)
	ownerFiber.pluginContext = newPluginContext(
		supervisor.runtime,
		ownerFiber,
		ownerFiber.rootScope,
		ownerFiber.lifetime,
		transaction,
	)
	declaration.current = ownerFiber
	declaration.lastFiberID = ownerFiber.id
	declaration.state = FiberStarting
	supervisor.setFiberState(ownerFiber, FiberStarting)

	applyErr := applyPlugin(applyContext, ownerFiber.instance, ownerFiber.pluginContext)
	if applyErr == nil {
		applyErr = transaction.commit()
	} else {
		applyErr = errors.Join(applyErr, transaction.rollback(applyContext))
	}
	if applyErr != nil {
		ownerFiber.cancel(applyErr)
		ownerFiber.rootScope.closeTree()
		ownerFiber.lastErr = applyErr
		declaration.lastErr = applyErr
		declaration.missing = nil
		declaration.state = FiberFailed
		supervisor.setFiberState(ownerFiber, FiberFailed)
		return fmt.Errorf("plugin: activate %s: %w", declaration.manifest.Name, applyErr)
	}

	declaration.lastErr = nil
	declaration.missing = nil
	declaration.state = FiberActive
	supervisor.setFiberState(ownerFiber, FiberActive)
	supervisor.attachDependencies(ownerFiber)
	return nil
}

func (supervisor *fiberSupervisor) unload(
	stopContext context.Context,
	pluginHandle Handle,
) error {
	supervisor.runtime.operations.Lock()
	defer supervisor.runtime.operations.Unlock()
	declaration, err := supervisor.findDeclaration(pluginHandle)
	if err != nil {
		return err
	}
	stopErr := supervisor.disposeDeclaration(stopContext, declaration)
	return errors.Join(stopErr, supervisor.reconcile(stopContext))
}

func (supervisor *fiberSupervisor) disposeDeclaration(
	stopContext context.Context,
	declaration *pluginDeclaration,
) error {
	if declaration == nil || declaration.removed {
		return nil
	}
	declaration.removed = true
	var stopErr error
	if declaration.current != nil {
		stopErr = supervisor.stopFiber(stopContext, declaration.current, FiberStopped)
	}
	declaration.current = nil
	declaration.state = FiberStopped
	declaration.missing = nil
	declaration.lastErr = stopErr
	if declaration.parent != nil {
		delete(declaration.parent.children, declaration.id)
	}
	if declaration.ownerEffect != nil {
		declaration.ownerEffect.state = fiberEffectDisposed
	}
	return stopErr
}

func (supervisor *fiberSupervisor) stopFiber(
	stopContext context.Context,
	ownerFiber *fiber,
	finalState FiberState,
) error {
	if ownerFiber == nil || ownerFiber.state == FiberStopping || ownerFiber.state == FiberStopped {
		return nil
	}
	supervisor.setFiberState(ownerFiber, FiberStopping)
	var stopErr error
	stopErr = errors.Join(stopErr, supervisor.stopDependents(stopContext, ownerFiber))

	ownerFiber.cancel(ErrContextClosed)
	ownerFiber.rootScope.closeTree()
	stopErr = errors.Join(stopErr, releaseFiberEffects(stopContext, ownerFiber.effects.entries))
	supervisor.detachDependencies(ownerFiber)
	supervisor.setFiberState(ownerFiber, finalState)
	ownerFiber.lastErr = stopErr
	return stopErr
}

func (supervisor *fiberSupervisor) replace(
	replaceContext context.Context,
	pluginHandle Handle,
	candidate Plugin,
) error {
	supervisor.runtime.operations.Lock()
	defer supervisor.runtime.operations.Unlock()
	declaration, err := supervisor.findDeclaration(pluginHandle)
	if err != nil {
		return err
	}
	if declaration.removed || declaration.state != FiberActive || declaration.current == nil {
		return ErrPluginNotActive
	}
	if candidate == nil {
		return errors.New("plugin: cannot replace with nil Plugin")
	}
	candidateManifest, err := pluginManifest(candidate)
	if err != nil {
		return err
	}
	if candidateManifest.Name != declaration.manifest.Name {
		return fmt.Errorf(
			"plugin: replacement name %q does not match %q",
			candidateManifest.Name,
			declaration.manifest.Name,
		)
	}
	if !sameServiceDefinitions(candidateManifest.Provides, declaration.manifest.Provides) {
		return errors.New("plugin: replacement must preserve the provided Service set")
	}
	supervisor.runtime.state.Lock()
	err = supervisor.runtime.services.admitManifest(candidateManifest)
	supervisor.runtime.state.Unlock()
	if err != nil {
		return err
	}

	previousFiber := declaration.current
	candidateFiber := supervisor.newFiber(declaration, candidate, candidateManifest)
	candidateFiber.rootScope = newReplacementRootScope(
		supervisor.runtime,
		candidateFiber,
		previousFiber.rootScope,
	)
	missing := supervisor.resolveDependencies(candidateFiber)
	if len(missing) != 0 {
		candidateFiber.cancel(ErrServiceUnavailable)
		candidateFiber.rootScope.closeTree()
		return fmt.Errorf("%w: %v", ErrServiceUnavailable, missing)
	}

	candidateTransaction := newMountTransaction(supervisor.runtime, candidateFiber, replaceContext)
	replacement := &replacementTransaction{
		previous:  previousFiber,
		candidate: candidateFiber,
	}
	candidateTransaction.replacement = replacement
	candidateFiber.pluginContext = newPluginContext(
		supervisor.runtime,
		candidateFiber,
		candidateFiber.rootScope,
		candidateFiber.lifetime,
		candidateTransaction,
	)
	supervisor.setFiberState(candidateFiber, FiberStarting)
	applyErr := applyPlugin(replaceContext, candidate, candidateFiber.pluginContext)
	if applyErr != nil {
		applyErr = errors.Join(applyErr, candidateTransaction.rollback(replaceContext))
		candidateFiber.cancel(applyErr)
		candidateFiber.rootScope.closeTree()
		supervisor.setFiberState(candidateFiber, FiberFailed)
		declaration.lastErr = applyErr
		return fmt.Errorf("plugin: prepare replacement %s: %w", candidateManifest.Name, applyErr)
	}

	dependentStopErr := supervisor.stopDependents(replaceContext, previousFiber)
	commitErr := candidateTransaction.commit()
	if commitErr != nil {
		candidateFiber.cancel(commitErr)
		candidateFiber.rootScope.closeTree()
		supervisor.setFiberState(candidateFiber, FiberFailed)
		reconcileErr := supervisor.reconcile(replaceContext)
		declaration.lastErr = commitErr
		return errors.Join(commitErr, dependentStopErr, reconcileErr)
	}

	declaration.instance = candidate
	declaration.manifest = candidateManifest
	declaration.current = candidateFiber
	declaration.lastFiberID = candidateFiber.id
	declaration.lastErr = nil
	declaration.missing = nil
	declaration.state = FiberActive
	supervisor.setFiberState(candidateFiber, FiberActive)
	supervisor.attachDependencies(candidateFiber)

	previousFiber.cancel(ErrContextClosed)
	previousFiber.rootScope.closeTree()
	retireErr := releaseFiberEffects(replaceContext, previousFiber.effects.entries)
	supervisor.detachDependencies(previousFiber)
	supervisor.setFiberState(previousFiber, FiberStopped)
	replacementErr := errors.Join(
		dependentStopErr,
		retireErr,
		supervisor.reconcile(replaceContext),
	)
	declaration.lastErr = replacementErr
	return replacementErr
}

func (supervisor *fiberSupervisor) shutdown(
	stopContext context.Context,
) error {
	supervisor.runtime.operations.Lock()
	defer supervisor.runtime.operations.Unlock()
	if supervisor.closed {
		return nil
	}
	supervisor.closed = true
	var shutdownErr error
	for declarationIndex := len(supervisor.declarations) - 1; declarationIndex >= 0; declarationIndex-- {
		declaration := supervisor.declarations[declarationIndex]
		if declaration.removed {
			continue
		}
		declaration.removed = true
		if declaration.current != nil {
			shutdownErr = errors.Join(
				shutdownErr,
				supervisor.stopFiber(stopContext, declaration.current, FiberStopped),
			)
		}
		declaration.current = nil
		declaration.state = FiberStopped
	}
	return shutdownErr
}

func (supervisor *fiberSupervisor) status(
	pluginHandle Handle,
) (FiberStatus, error) {
	supervisor.runtime.operations.Lock()
	defer supervisor.runtime.operations.Unlock()
	declaration, err := supervisor.findDeclaration(pluginHandle)
	if err != nil {
		return FiberStatus{}, err
	}
	return supervisor.statusLocked(declaration), nil
}

func (supervisor *fiberSupervisor) statuses() []FiberStatus {
	supervisor.runtime.operations.Lock()
	defer supervisor.runtime.operations.Unlock()
	snapshots := make([]FiberStatus, 0, len(supervisor.declarations))
	for _, declaration := range supervisor.declarations {
		snapshots = append(snapshots, supervisor.statusLocked(declaration))
	}
	return snapshots
}

func (supervisor *fiberSupervisor) findDeclaration(pluginHandle Handle) (*pluginDeclaration, error) {
	if pluginHandle.owner != supervisor.runtime || pluginHandle.id == 0 {
		return nil, errors.New("plugin: Handle does not belong to this Runtime")
	}
	declaration, exists := supervisor.byHandle[pluginHandle.id]
	if !exists {
		return nil, errors.New("plugin: unknown Handle")
	}
	return declaration, nil
}

func (supervisor *fiberSupervisor) parentReady(declaration *pluginDeclaration) bool {
	if declaration.parent == nil {
		return true
	}
	return !declaration.parent.removed &&
		declaration.parent.state == FiberActive &&
		declaration.parent.current != nil &&
		declaration.parent.current.id == declaration.parentFiberID
}

func (supervisor *fiberSupervisor) missingRequiredServices(
	declaration *pluginDeclaration,
) []string {
	probeScope := declaration.parentScope
	if probeScope == nil {
		probeScope = &Scope{
			runtime: supervisor.runtime,
		}
	}
	missing := make([]string, 0)
	supervisor.runtime.state.RLock()
	for _, requiredRef := range declaration.manifest.Requires {
		if _, exists := supervisor.runtime.services.resolve(requiredRef, probeScope); !exists {
			missing = append(missing, requiredRef.name)
		}
	}
	supervisor.runtime.state.RUnlock()
	sort.Strings(missing)
	return missing
}

func (supervisor *fiberSupervisor) resolveDependencies(ownerFiber *fiber) []string {
	missing := make([]string, 0)
	supervisor.runtime.state.RLock()
	for _, requiredRef := range ownerFiber.manifest.Requires {
		binding, exists := supervisor.runtime.services.resolve(requiredRef, ownerFiber.rootScope)
		if !exists || bindingOwner(binding) == nil {
			missing = append(missing, requiredRef.name)
			continue
		}
		ownerFiber.dependencies[requiredRef.token] = resolvedServiceDependency{
			reference: requiredRef,
			binding:   binding,
			provider:  bindingOwner(binding),
		}
	}
	for _, optionalRef := range ownerFiber.manifest.Optional {
		binding, exists := supervisor.runtime.services.resolve(optionalRef, ownerFiber.rootScope)
		if !exists || bindingOwner(binding) == nil {
			continue
		}
		ownerFiber.dependencies[optionalRef.token] = resolvedServiceDependency{
			reference: optionalRef,
			binding:   binding,
			provider:  bindingOwner(binding),
			optional:  true,
		}
	}
	supervisor.runtime.state.RUnlock()
	sort.Strings(missing)
	return missing
}

func (supervisor *fiberSupervisor) dependenciesChanged(ownerFiber *fiber) bool {
	supervisor.runtime.state.RLock()
	defer supervisor.runtime.state.RUnlock()
	for _, requiredRef := range ownerFiber.manifest.Requires {
		binding, exists := supervisor.runtime.services.resolve(requiredRef, ownerFiber.rootScope)
		dependency, recorded := ownerFiber.dependencies[requiredRef.token]
		if !exists || !recorded || dependency.binding != binding {
			return true
		}
	}
	for _, optionalRef := range ownerFiber.manifest.Optional {
		binding, exists := supervisor.runtime.services.resolve(optionalRef, ownerFiber.rootScope)
		dependency, recorded := ownerFiber.dependencies[optionalRef.token]
		if exists != recorded || (exists && dependency.binding != binding) {
			return true
		}
	}
	return false
}

func (supervisor *fiberSupervisor) attachDependencies(ownerFiber *fiber) {
	for _, dependency := range ownerFiber.dependencies {
		if dependency.provider == nil {
			continue
		}
		consumers := supervisor.dependents[dependency.provider.id]
		if consumers == nil {
			consumers = make(map[FiberID]*fiber)
			supervisor.dependents[dependency.provider.id] = consumers
		}
		consumers[ownerFiber.id] = ownerFiber
	}
}

func (supervisor *fiberSupervisor) detachDependencies(ownerFiber *fiber) {
	for _, dependency := range ownerFiber.dependencies {
		if dependency.provider == nil {
			continue
		}
		consumers := supervisor.dependents[dependency.provider.id]
		delete(consumers, ownerFiber.id)
		if len(consumers) == 0 {
			delete(supervisor.dependents, dependency.provider.id)
		}
	}
	delete(supervisor.dependents, ownerFiber.id)
}

func (supervisor *fiberSupervisor) stopDependents(
	stopContext context.Context,
	providerFiber *fiber,
) error {
	consumers := make([]*fiber, 0, len(supervisor.dependents[providerFiber.id]))
	for _, consumerFiber := range supervisor.dependents[providerFiber.id] {
		consumers = append(consumers, consumerFiber)
	}
	sort.Slice(consumers, func(leftIndex int, rightIndex int) bool {
		return consumers[leftIndex].order > consumers[rightIndex].order
	})
	var stopErr error
	for _, consumerFiber := range consumers {
		stopErr = errors.Join(
			stopErr,
			supervisor.stopForDependencyChange(stopContext, consumerFiber),
		)
	}
	return stopErr
}

func (supervisor *fiberSupervisor) stopForDependencyChange(
	stopContext context.Context,
	ownerFiber *fiber,
) error {
	declaration := ownerFiber.declaration
	stopErr := supervisor.stopFiber(stopContext, ownerFiber, FiberStopped)
	if declaration != nil && !declaration.removed && declaration.current == ownerFiber {
		declaration.current = nil
		declaration.state = FiberWaiting
		declaration.lastErr = stopErr
		declaration.missing = supervisor.missingRequiredServices(declaration)
	}
	return stopErr
}

func (supervisor *fiberSupervisor) setFiberState(ownerFiber *fiber, nextState FiberState) {
	supervisor.runtime.state.Lock()
	ownerFiber.state = nextState
	supervisor.runtime.state.Unlock()
}

func (supervisor *fiberSupervisor) statusLocked(declaration *pluginDeclaration) FiberStatus {
	statusView := FiberStatus{
		HandleID: declaration.id,
		FiberID:  declaration.lastFiberID,
		Name:     declaration.manifest.Name,
		State:    declaration.state,
		Missing:  append([]string(nil), declaration.missing...),
		Error:    declaration.lastErr,
	}
	ownerFiber := declaration.current
	if ownerFiber == nil {
		return statusView
	}
	statusView.Effects = ownerFiber.effects.labels()
	for _, dependency := range ownerFiber.dependencies {
		statusView.Dependencies = append(
			statusView.Dependencies,
			ServiceDependencyStatus{
				Service:         dependency.reference.name,
				ProviderFiberID: dependency.provider.id,
				Optional:        dependency.optional,
			},
		)
	}
	for _, ownership := range ownerFiber.effects.entries {
		if ownership.registration == nil || ownership.state != fiberEffectActive {
			continue
		}
		entryView := ownership.registration.diagnostic()
		switch entryView.kind {
		case runtimeEntryService:
			statusView.Services = append(
				statusView.Services,
				ServiceBindingStatus{
					Service: entryView.name,
					Scope:   ownership.scope.target,
				},
			)
		case runtimeEntryWaterfall:
			statusView.Waterfalls = append(
				statusView.Waterfalls,
				WaterfallBindingStatus{
					Waterfall: entryView.name,
					Scope:     ownership.scope.target,
				},
			)
		case runtimeEntryEvent:
			statusView.Events = append(
				statusView.Events,
				EventSubscriptionStatus{
					Event: entryView.name,
					Scope: ownership.scope.target,
				},
			)
		}
	}
	sort.Slice(statusView.Dependencies, func(leftIndex int, rightIndex int) bool {
		return statusView.Dependencies[leftIndex].Service < statusView.Dependencies[rightIndex].Service
	})
	return statusView
}

func pluginManifest(instance Plugin) (metadata manifestSpec, manifestErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			manifestErr = fmt.Errorf("plugin: Manifest panicked: %v", recovered)
		}
	}()
	return normalizeManifest(instance.Manifest())
}

func applyPlugin(
	applyContext context.Context,
	instance Plugin,
	pluginContext *Context,
) (applyErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applyErr = fmt.Errorf("plugin: Apply panicked: %v", recovered)
		}
	}()
	return instance.Apply(applyContext, pluginContext)
}

func invokePluginDispose(
	disposeContext context.Context,
	instance Plugin,
) (disposeErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			disposeErr = fmt.Errorf("plugin: Dispose panicked: %v", recovered)
		}
	}()
	return instance.Dispose(disposeContext)
}

func bindingOwner(binding serviceBinding) *fiber {
	if binding == nil {
		return nil
	}
	return binding.entryOwner()
}
