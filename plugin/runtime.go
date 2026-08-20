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

// Runtime owns Plugin lifecycle, Scope visibility, dependency settlement, and
// all reversible Service, Event, and Waterfall contributions.
type Runtime struct {
	operations sync.Mutex
	state      sync.RWMutex

	rootScope  *scope
	mounts     []*mountedPlugin
	byHandle   map[uint64]*mountedPlugin
	services   *serviceRegistry
	events     *eventRegistry
	waterfalls *waterfallRegistry

	eventFailures EventFailureReporter
	nextHandle    uint64
	nextFiber     FiberID
	nextOrdinal   uint64
	started       bool
	closed        bool
}

type activation struct {
	runtime  *Runtime
	fiber    *fiber
	lifetime context.Context
}

type mountedPlugin struct {
	handleID uint64
	parent   *mountedPlugin
	children []*mountedPlugin
	scope    *scope
	instance Plugin
	manifest manifestSpec
	current  *fiber
	removed  bool
}

// NewRuntime creates one isolated Plugin Runtime.
func NewRuntime(settings RuntimeSettings) *Runtime {
	return &Runtime{
		rootScope:     newRootScope(),
		byHandle:      make(map[uint64]*mountedPlugin),
		services:      newServiceRegistry(),
		events:        newEventRegistry(),
		waterfalls:    newWaterfallRegistry(),
		eventFailures: settings.EventFailures,
	}
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

	startIndex := len(runtimeEngine.mounts)
	handles := make([]Handle, 0, len(instances))
	for _, pluginInstance := range instances {
		mounted, err := runtimeEngine.admit(pluginInstance, nil)
		if err != nil {
			rollbackErr := runtimeEngine.rollbackFrom(startContext, startIndex)
			return nil, errors.Join(err, rollbackErr)
		}
		handles = append(handles, Handle{
			owner: runtimeEngine,
			id:    mounted.handleID,
		})
	}
	settleErr := runtimeEngine.reconcile(startContext)
	readinessErr := runtimeEngine.readinessError(runtimeEngine.mounts[startIndex:])
	if startErr := errors.Join(settleErr, readinessErr); startErr != nil {
		rollbackErr := runtimeEngine.rollbackFrom(startContext, startIndex)
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
	return runtimeEngine.mount(mountContext, Handle{}, pluginInstance)
}

// MountChild adds a Plugin in a child Scope owned by parent.
func (runtimeEngine *Runtime) MountChild(
	mountContext context.Context,
	parent Handle,
	pluginInstance Plugin,
) (Handle, error) {
	return runtimeEngine.mount(mountContext, parent, pluginInstance)
}

func (runtimeEngine *Runtime) mount(
	mountContext context.Context,
	parent Handle,
	pluginInstance Plugin,
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

	var parentMount *mountedPlugin
	if parent.owner != nil || parent.id != 0 {
		if parent.owner != runtimeEngine {
			return Handle{}, errors.New("plugin: parent Handle belongs to another Runtime")
		}
		parentMount = runtimeEngine.byHandle[parent.id]
		if parentMount == nil || parentMount.removed || parentMount.current == nil ||
			parentMount.current.state != FiberActive {
			return Handle{}, errors.New("plugin: parent Plugin is not active")
		}
	}
	mounted, err := runtimeEngine.admit(pluginInstance, parentMount)
	if err != nil {
		return Handle{}, err
	}
	settleErr := runtimeEngine.reconcile(mountContext)
	readinessErr := runtimeEngine.readinessError([]*mountedPlugin{mounted})
	if mountErr := errors.Join(settleErr, readinessErr); mountErr != nil {
		rollbackErr := runtimeEngine.removeMounted(
			mountContext,
			mounted,
		)
		_ = runtimeEngine.reconcile(mountContext)
		return Handle{}, errors.Join(mountErr, rollbackErr)
	}
	return Handle{
		owner: runtimeEngine,
		id:    mounted.handleID,
	}, nil
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
	runtimeEngine.markRemovedTree(mounted)
	stopErr := runtimeEngine.stopFiberWithDependents(
		stopContext,
		mounted.current,
		make(map[*fiber]struct{}),
	)
	runtimeEngine.deleteMountTree(mounted)
	runtimeEngine.prepareStopped()
	settleErr := runtimeEngine.reconcile(stopContext)
	return errors.Join(stopErr, settleErr)
}

// Replace prepares a contract-compatible candidate while the current Plugin
// remains active, then swaps Runtime contributions and reactivates dependents.
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
	candidateManifest, err := normalizeManifest(candidate)
	if err != nil {
		return err
	}
	if !sameManifestContract(mounted.manifest, candidateManifest) {
		return errors.New("plugin: replacement changes the mounted Plugin contract")
	}
	candidateFiber := runtimeEngine.newFiber(
		mounted,
		candidate,
		candidateManifest,
	)
	dependencies, missing, blocked := runtimeEngine.resolveDependencies(mounted)
	if len(missing) != 0 || blocked {
		return errors.New("plugin: replacement dependencies are unavailable")
	}
	if err := runtimeEngine.prepareFiber(
		replaceContext,
		candidateFiber,
		dependencies,
	); err != nil {
		return err
	}

	previousFiber := mounted.current
	stopErr := runtimeEngine.stopDependents(
		replaceContext,
		previousFiber,
		make(map[*fiber]struct{}),
	)
	runtimeEngine.state.Lock()
	runtimeEngine.withdrawContributionsLocked(previousFiber)
	if publicationErr := runtimeEngine.publishContributionsLocked(candidateFiber); publicationErr != nil {
		runtimeEngine.publishExistingContributionsLocked(previousFiber)
		runtimeEngine.state.Unlock()
		rollbackErr := runtimeEngine.rollbackPrepared(
			replaceContext,
			candidateFiber,
			publicationErr,
		)
		runtimeEngine.prepareStopped()
		_ = runtimeEngine.reconcile(replaceContext)
		return errors.Join(stopErr, publicationErr, rollbackErr)
	}
	previousFiber.state = FiberStopping
	candidateFiber.state = FiberActive
	mounted.instance = candidate
	mounted.manifest = candidateManifest
	mounted.current = candidateFiber
	runtimeEngine.state.Unlock()

	previousFiber.cancel(ErrPluginNotActive)
	disposeErr := previousFiber.effects.release(replaceContext)
	previousFiber.instance.RuntimePlugin().detach(previousFiber.activation)
	runtimeEngine.state.Lock()
	previousFiber.state = FiberStopped
	previousFiber.lastErr = disposeErr
	runtimeEngine.state.Unlock()

	runtimeEngine.prepareStopped()
	settleErr := runtimeEngine.reconcile(replaceContext)
	return errors.Join(stopErr, disposeErr, settleErr)
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
	for _, mounted := range runtimeEngine.mounts {
		mounted.removed = true
	}
	var shutdownErr error
	visited := make(map[*fiber]struct{})
	for mountIndex := len(runtimeEngine.mounts) - 1; mountIndex >= 0; mountIndex-- {
		shutdownErr = errors.Join(
			shutdownErr,
			runtimeEngine.stopFiberWithDependents(
				stopContext,
				runtimeEngine.mounts[mountIndex].current,
				visited,
			),
		)
	}
	runtimeEngine.mounts = nil
	runtimeEngine.byHandle = make(map[uint64]*mountedPlugin)
	return shutdownErr
}

// Status returns immutable diagnostics for one mounted Plugin.
func (runtimeEngine *Runtime) Status(pluginHandle Handle) (FiberStatus, error) {
	if runtimeEngine == nil {
		return FiberStatus{}, errors.New("plugin: status through nil Runtime")
	}
	runtimeEngine.operations.Lock()
	defer runtimeEngine.operations.Unlock()
	runtimeEngine.state.RLock()
	defer runtimeEngine.state.RUnlock()
	if pluginHandle.owner != runtimeEngine {
		return FiberStatus{}, errors.New("plugin: Handle belongs to another Runtime")
	}
	mounted := runtimeEngine.byHandle[pluginHandle.id]
	if mounted == nil || mounted.removed {
		return FiberStatus{}, errors.New("plugin: Handle is not mounted")
	}
	return statusOf(mounted), nil
}

// Statuses returns diagnostics in mount order.
func (runtimeEngine *Runtime) Statuses() []FiberStatus {
	if runtimeEngine == nil {
		return nil
	}
	runtimeEngine.operations.Lock()
	defer runtimeEngine.operations.Unlock()
	runtimeEngine.state.RLock()
	defer runtimeEngine.state.RUnlock()
	snapshots := make([]FiberStatus, 0, len(runtimeEngine.mounts))
	for _, mounted := range runtimeEngine.mounts {
		if !mounted.removed {
			snapshots = append(snapshots, statusOf(mounted))
		}
	}
	return snapshots
}

func (runtimeEngine *Runtime) lookup(pluginHandle Handle) (*mountedPlugin, error) {
	if pluginHandle.owner != runtimeEngine {
		return nil, errors.New("plugin: Handle belongs to another Runtime")
	}
	mounted := runtimeEngine.byHandle[pluginHandle.id]
	if mounted == nil || mounted.removed {
		return nil, errors.New("plugin: Handle is not mounted")
	}
	return mounted, nil
}

func activationOf(owner Plugin) (*activation, error) {
	if owner == nil || owner.RuntimePlugin() == nil {
		return nil, ErrPluginNotBound
	}
	selectedActivation := owner.RuntimePlugin().currentActivation()
	if selectedActivation == nil || selectedActivation.runtime == nil ||
		selectedActivation.fiber == nil {
		return nil, ErrPluginNotBound
	}
	return selectedActivation, nil
}

func activeActivationOf(owner Plugin) (*activation, error) {
	selectedActivation, err := activationOf(owner)
	if err != nil {
		return nil, err
	}
	runtimeEngine := selectedActivation.runtime
	runtimeEngine.state.RLock()
	active := selectedActivation.fiber.state == FiberActive
	runtimeEngine.state.RUnlock()
	if !active {
		return nil, ErrPluginNotActive
	}
	return selectedActivation, nil
}

var closedLifetime = func() context.Context {
	closedContext, cancelLifetime := context.WithCancelCause(context.Background())
	cancelLifetime(ErrPluginNotBound)
	return closedContext
}()

// Lifetime returns the current Plugin activation lifetime.
func Lifetime(owner Plugin) context.Context {
	selectedActivation, err := activationOf(owner)
	if err != nil || selectedActivation.lifetime == nil {
		return closedLifetime
	}
	return selectedActivation.lifetime
}

func statusOf(mounted *mountedPlugin) FiberStatus {
	ownerFiber := mounted.current
	if ownerFiber == nil {
		return FiberStatus{
			HandleID: mounted.handleID,
			Name:     mounted.manifest.name,
			State:    FiberStopped,
		}
	}
	diagnostics := FiberStatus{
		HandleID: mounted.handleID,
		FiberID:  ownerFiber.id,
		Name:     ownerFiber.manifest.name,
		State:    ownerFiber.state,
		Missing:  append([]string(nil), ownerFiber.missing...),
		Error:    ownerFiber.lastErr,
		Effects:  ownerFiber.effects.labels(),
	}
	dependencyKeys := make([]string, 0, len(ownerFiber.dependencies))
	dependencyByName := make(map[string]*serviceDependency)
	for _, dependency := range ownerFiber.dependencies {
		dependencyKeys = append(dependencyKeys, dependency.reference.name)
		dependencyByName[dependency.reference.name] = dependency
	}
	sort.Strings(dependencyKeys)
	for _, dependencyName := range dependencyKeys {
		dependency := dependencyByName[dependencyName]
		diagnostics.Dependencies = append(diagnostics.Dependencies, ServiceDependencyStatus{
			Service:         dependency.reference.name,
			ProviderFiberID: dependency.binding.owner.id,
			Optional:        dependency.optional,
		})
	}
	if ownerFiber.state == FiberActive {
		for _, offer := range ownerFiber.manifest.provides {
			diagnostics.Services = append(diagnostics.Services, ServiceBindingStatus{
				Service: offer.reference.name,
				Scope:   ownerFiber.scope.key,
			})
		}
		for _, offer := range ownerFiber.manifest.events {
			diagnostics.Events = append(diagnostics.Events, EventSubscriptionStatus{
				Event: offer.reference.name,
				Scope: ownerFiber.scope.key,
			})
		}
		for _, offer := range ownerFiber.manifest.waterfalls {
			diagnostics.Waterfalls = append(diagnostics.Waterfalls, WaterfallBindingStatus{
				Waterfall: offer.reference.name,
				Scope:     ownerFiber.scope.key,
			})
		}
	}
	return diagnostics
}
