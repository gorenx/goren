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

// DescendantLifecycle owns managed runtime descendants of an exact Agent.
type DescendantLifecycle interface {
	HasRuntimeDescendants(Agent) bool
	CloseDescendants(context.Context, Agent) error
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
	registry *RegistryService
	factory  Factory
	once     sync.Once
}

func (registration *factoryRegistration) Close() {
	if registration == nil {
		return
	}
	registration.once.Do(func() {
		registration.registry.closeFactory(registration)
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
		registry: service,
		factory:  agentFactory,
	}
	service.factory = registration
	return registration, nil
}

func (service *RegistryService) closeFactory(
	registration *factoryRegistration,
) {
	service.mutex.Lock()
	if service.factory == registration {
		service.admission = registryShuttingDown
		service.factory = nil
		service.coordinator.cancelConstructions()
	}
	service.mutex.Unlock()
}

func (service *RegistryService) reserve(
	identifier session.SessionID,
	parent Agent,
) (Factory, *reservation, error) {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.admission != registryAccepting {
		return nil, nil, errors.New("agent: Agent Registry is shutting down")
	}
	registration := service.factory
	if registration == nil {
		return nil, nil, errors.New(
			"agent: no Agent factory attached; mount an Agent Loop Plugin",
		)
	}
	pending, err := service.coordinator.reserve(identifier, parent)
	if err != nil {
		return nil, nil, err
	}
	return registration.factory, pending, nil
}

// Create constructs, publishes, and commits one fresh exact Agent epoch.
func (service *RegistryService) Create(
	requestContext context.Context,
	settings CreateOptions,
) (Handle, error) {
	if requestContext == nil {
		return Handle{}, errors.New("agent: Create Context is nil")
	}
	agentFactory, pending, err := service.reserve(
		settings.SessionID,
		settings.RuntimeParent,
	)
	if err != nil {
		return Handle{}, err
	}
	if err = agentFactory.CreateAgent(requestContext, pending, settings); err != nil {
		closeErr := service.coordinator.abort(
			context.WithoutCancel(requestContext),
			pending,
		)
		return Handle{}, errors.Join(err, closeErr)
	}
	if err = service.coordinator.activate(
		requestContext,
		pending,
		SessionStartup,
	); err != nil {
		closeErr := service.coordinator.abort(
			context.WithoutCancel(requestContext),
			pending,
		)
		return Handle{}, errors.Join(err, closeErr)
	}
	return service.coordinator.handle(pending), nil
}

// Resume reconstructs, publishes, and commits one durable exact Agent epoch.
func (service *RegistryService) Resume(
	requestContext context.Context,
	settings ResumeOptions,
) (Handle, error) {
	if requestContext == nil {
		return Handle{}, errors.New("agent: Resume Context is nil")
	}
	agentFactory, pending, err := service.reserve(
		settings.SessionID,
		settings.RuntimeParent,
	)
	if err != nil {
		return Handle{}, err
	}
	if err = agentFactory.ResumeAgent(requestContext, pending, settings); err != nil {
		closeErr := service.coordinator.abort(
			context.WithoutCancel(requestContext),
			pending,
		)
		return Handle{}, errors.Join(err, closeErr)
	}
	if err = service.coordinator.activate(
		requestContext,
		pending,
		SessionResume,
	); err != nil {
		closeErr := service.coordinator.abort(
			context.WithoutCancel(requestContext),
			pending,
		)
		return Handle{}, errors.Join(err, closeErr)
	}
	return service.coordinator.handle(pending), nil
}

// Get returns the visible exact Agent registered for identifier.
func (service *RegistryService) Get(identifier session.SessionID) (Agent, bool) {
	return service.coordinator.liveAgent(identifier)
}

// Contains reports whether subject is the visible exact Agent epoch.
func (service *RegistryService) Contains(subject Agent) bool {
	return service.coordinator.contains(subject)
}

// List returns visible exact Agents in reservation order.
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

// CloseDescendants permanently stops admission below parent and closes every
// existing exact runtime descendant child-first.
func (service *RegistryService) CloseDescendants(
	closeContext context.Context,
	parent Agent,
) error {
	return service.coordinator.closeDescendants(closeContext, parent)
}

// Shutdown stops construction admission and closes every Agent epoch
// child-first, including reservations whose Factory is still returning.
func (service *RegistryService) Shutdown(closeContext context.Context) error {
	service.mutex.Lock()
	service.admission = registryShuttingDown
	service.mutex.Unlock()
	return service.coordinator.closeAll(closeContext)
}

var _ Registry = (*RegistryService)(nil)
var _ Constructor = (*RegistryService)(nil)
var _ ScopeProvisioning = (*RegistryService)(nil)
var _ DescendantLifecycle = (*RegistryService)(nil)
var _ FactoryRegistrar = (*RegistryService)(nil)
