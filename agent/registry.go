package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/session"
)

// RegistryOptions supplies observer containment without changing lifecycle
// semantics.
type RegistryOptions struct {
	ObserverError func(error)
}

// Registry is the read-only view of currently visible exact Agent epochs.
type Registry interface {
	Get(session.SessionID) (Agent, bool)
	Contains(Agent) bool
	List() []Agent
}

// Constructor owns fresh Agent creation and durable Agent restoration.
type Constructor interface {
	Create(context.Context, CreateOptions) (Handle, error)
	Resume(context.Context, ResumeOptions) (Handle, error)
}

// ScopeProvisioning installs one extension transaction into an exact live
// Agent Scope.
type ScopeProvisioning interface {
	Provision(context.Context, Agent, Provisioner) error
}

// RuntimeDescendants observes the managed runtime descendants of an exact
// Agent. Closing remains an internal consequence of exact Handle disposal or
// Registry shutdown rather than a separate consumer command.
type RuntimeDescendants interface {
	HasRuntimeDescendants(Agent) bool
}

// FactoryRegistrar accepts the process-wide Agent construction implementation.
type FactoryRegistrar interface {
	RegisterFactory(Factory) (FactoryRegistration, error)
}

// FactoryRegistration owns one exact Factory registration.
type FactoryRegistration interface {
	Close()
}

type factoryRegistration struct {
	registry  *RegistryService
	factory   Factory
	epochs    map[*epoch]struct{}
	state     factoryRegistrationState
	closeDone lifecycleSignal
	once      sync.Once
}

func (registration *factoryRegistration) Close() {
	if registration == nil {
		return
	}
	registration.once.Do(func() {
		done := registration.registry.closeFactory(registration)
		<-done
	})
}

// RegistryService is the single Agent lifecycle application service. It owns
// Factory routing and delegates all epoch state to LifecycleCoordinator while
// exposing separate consumer-facing capabilities.
type RegistryService struct {
	mutex       sync.RWMutex
	admission   registryAdmission
	factory     *factoryRegistration
	coordinator *LifecycleCoordinator
	shutdown    lifecycleSignal
	shutdownErr error
}

// construction is one Registry-admitted invocation of the currently
// registered Factory against an exact Agent epoch.
type construction struct {
	registry     *RegistryService
	registration *factoryRegistration
	factory      Factory
	epoch        *epoch
	once         sync.Once
}

func (admitted *construction) finish() {
	if admitted == nil || admitted.registry == nil || admitted.registration == nil {
		return
	}
	admitted.once.Do(func() {
		admitted.registry.finishConstruction(
			admitted.registration,
			admitted.epoch,
		)
	})
}

// NewRegistry constructs an empty Agent lifecycle Service.
func NewRegistry(settings RegistryOptions) *RegistryService {
	reporter := settings.ObserverError
	if reporter == nil {
		reporter = func(error) {}
	}
	return &RegistryService{
		admission:   registryAccepting,
		coordinator: newLifecycleCoordinator(reporter),
		shutdown:    newLifecycleSignal(),
	}
}

// RegisterFactory installs one exact Agent construction implementation.
func (service *RegistryService) RegisterFactory(
	agentFactory Factory,
) (FactoryRegistration, error) {
	if agentFactory == nil {
		return nil, errors.New("agent: Agent factory is required")
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.admission != registryAccepting {
		return nil, errors.New("agent: Agent Registry is shutting down")
	}
	if service.factory != nil {
		return nil, errors.New("agent: an Agent factory is already registered")
	}
	registration := &factoryRegistration{
		registry:  service,
		factory:   agentFactory,
		epochs:    make(map[*epoch]struct{}),
		state:     factoryRegistered,
		closeDone: newLifecycleSignal(),
	}
	service.factory = registration
	return registration, nil
}

func (service *RegistryService) closeFactory(
	registration *factoryRegistration,
) <-chan struct{} {
	service.mutex.Lock()
	if registration.state != factoryRegistered {
		done := registration.closeDone.done
		service.mutex.Unlock()
		return done
	}
	registration.state = factoryRegistrationClosing
	if service.factory == registration {
		service.factory = nil
	}
	epochs := make([]*epoch, 0, len(registration.epochs))
	for target := range registration.epochs {
		epochs = append(epochs, target)
	}
	if len(epochs) == 0 {
		registration.state = factoryRegistrationClosed
		registration.closeDone.close()
	}
	done := registration.closeDone.done
	service.mutex.Unlock()
	service.coordinator.cancelEpochConstructions(epochs)
	return done
}

func (service *RegistryService) beginConstruction(
	identifier session.SessionID,
	parent Agent,
) (*construction, error) {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.admission != registryAccepting {
		return nil, errors.New("agent: Agent Registry is shutting down")
	}
	registration := service.factory
	if registration == nil {
		return nil, errors.New(
			"agent: no Agent factory attached; mount an Agent Loop Plugin",
		)
	}
	target, err := service.coordinator.createEpoch(identifier, parent)
	if err != nil {
		return nil, err
	}
	registration.epochs[target] = struct{}{}
	return &construction{
		registry:     service,
		registration: registration,
		factory:      registration.factory,
		epoch:        target,
	}, nil
}

func (service *RegistryService) finishConstruction(
	registration *factoryRegistration,
	target *epoch,
) {
	service.mutex.Lock()
	delete(registration.epochs, target)
	if registration.state == factoryRegistrationClosing &&
		len(registration.epochs) == 0 {
		registration.state = factoryRegistrationClosed
		registration.closeDone.close()
	}
	service.mutex.Unlock()
}

// Create constructs, publishes, and commits one fresh exact Agent epoch.
func (service *RegistryService) Create(
	requestContext context.Context,
	settings CreateOptions,
) (Handle, error) {
	if requestContext == nil {
		return Handle{}, errors.New("agent: Create Context is nil")
	}
	admittedConstruction, err := service.beginConstruction(
		settings.SessionID,
		settings.RuntimeParent,
	)
	if err != nil {
		return Handle{}, err
	}
	defer admittedConstruction.finish()
	if err = admittedConstruction.factory.CreateAgent(
		requestContext,
		admittedConstruction.epoch,
		settings,
	); err != nil {
		closeErr := service.coordinator.abort(
			context.WithoutCancel(requestContext),
			admittedConstruction.epoch,
		)
		return Handle{}, errors.Join(err, closeErr)
	}
	if err = service.coordinator.activate(
		requestContext,
		admittedConstruction.epoch,
		SessionStartup,
	); err != nil {
		closeErr := service.coordinator.abort(
			context.WithoutCancel(requestContext),
			admittedConstruction.epoch,
		)
		return Handle{}, errors.Join(err, closeErr)
	}
	return service.coordinator.handle(admittedConstruction.epoch), nil
}

// Resume reconstructs, publishes, and commits one durable exact Agent epoch.
func (service *RegistryService) Resume(
	requestContext context.Context,
	settings ResumeOptions,
) (Handle, error) {
	if requestContext == nil {
		return Handle{}, errors.New("agent: Resume Context is nil")
	}
	admittedConstruction, err := service.beginConstruction(
		settings.SessionID,
		settings.RuntimeParent,
	)
	if err != nil {
		return Handle{}, err
	}
	defer admittedConstruction.finish()
	if err = admittedConstruction.factory.ResumeAgent(
		requestContext,
		admittedConstruction.epoch,
		settings,
	); err != nil {
		closeErr := service.coordinator.abort(
			context.WithoutCancel(requestContext),
			admittedConstruction.epoch,
		)
		return Handle{}, errors.Join(err, closeErr)
	}
	if err = service.coordinator.activate(
		requestContext,
		admittedConstruction.epoch,
		SessionResume,
	); err != nil {
		closeErr := service.coordinator.abort(
			context.WithoutCancel(requestContext),
			admittedConstruction.epoch,
		)
		return Handle{}, errors.Join(err, closeErr)
	}
	return service.coordinator.handle(admittedConstruction.epoch), nil
}

// Get returns the visible exact Agent registered for identifier.
func (service *RegistryService) Get(identifier session.SessionID) (Agent, bool) {
	return service.coordinator.liveAgent(identifier)
}

// Contains reports whether subject is the visible exact Agent epoch.
func (service *RegistryService) Contains(subject Agent) bool {
	return service.coordinator.contains(subject)
}

// List returns visible exact Agents in epoch creation order.
func (service *RegistryService) List() []Agent {
	return service.coordinator.liveAgents()
}

// Provision applies one extension transaction to an exact live Agent Scope.
func (service *RegistryService) Provision(
	requestContext context.Context,
	subject Agent,
	source Provisioner,
) error {
	if source == nil {
		return errors.New("agent: Agent Provisioner is nil")
	}
	target, err := service.coordinator.epochForAgent(subject)
	if err != nil {
		return err
	}
	service.coordinator.mutex.Lock()
	live := target.phase == epochLive
	runtime := target.runtime
	service.coordinator.mutex.Unlock()
	if !live || runtime == nil {
		return fmt.Errorf("agent: Agent %q is not accepting Provisioning", subject.ID())
	}
	return runtime.Provision(requestContext, source)
}

// HasRuntimeDescendants reports whether the exact Agent currently owns any
// managed runtime descendant. Durable Session lineage is not consulted.
func (service *RegistryService) HasRuntimeDescendants(subject Agent) bool {
	return service.coordinator.hasDescendants(subject)
}

// Shutdown stops construction admission and closes every Agent epoch
// child-first, including epochs whose Factory is still returning.
func (service *RegistryService) Shutdown(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	service.mutex.Lock()
	switch service.admission {
	case registryClosed:
		closeErr := service.shutdownErr
		service.mutex.Unlock()
		return closeErr
	case registryDraining:
		done := service.shutdown.done
		service.mutex.Unlock()
		select {
		case <-done:
			service.mutex.RLock()
			closeErr := service.shutdownErr
			service.mutex.RUnlock()
			return closeErr
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	case registryAccepting:
		service.admission = registryDraining
		service.factory = nil
	}
	service.mutex.Unlock()
	service.coordinator.cancelConstructions()
	closeErr := service.coordinator.closeAll(
		context.WithoutCancel(closeContext),
	)
	service.mutex.Lock()
	service.shutdownErr = closeErr
	service.admission = registryClosed
	service.shutdown.close()
	service.mutex.Unlock()
	return closeErr
}

var _ Registry = (*RegistryService)(nil)
var _ Constructor = (*RegistryService)(nil)
var _ ScopeProvisioning = (*RegistryService)(nil)
var _ RuntimeDescendants = (*RegistryService)(nil)
var _ FactoryRegistrar = (*RegistryService)(nil)
