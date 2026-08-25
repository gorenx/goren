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

// RuntimeLifecycle is the narrow integration port used by the Agent Loop
// Plugin that supplies construction and coordinates process shutdown.
type RuntimeLifecycle interface {
	RegisterFactory(Factory) (FactoryRegistration, error)
	Shutdown(context.Context) error
}

// FactoryRegistration owns one exact Factory registration.
type FactoryRegistration interface {
	Unregister()
}

type factoryRegistration struct {
	registry *RegistryService
	factory  Factory
	once     sync.Once
}

func (registration *factoryRegistration) Unregister() {
	if registration == nil {
		return
	}
	registration.once.Do(func() {
		registration.registry.unregisterFactory(registration)
	})
}

// RegistryService is the single Agent lifecycle application service. It owns
// Factory routing and delegates all epoch state to LifecycleCoordinator while
// exposing separate consumer-facing capabilities.
type RegistryService struct {
	mutex       sync.RWMutex
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

func (service *RegistryService) unregisterFactory(
	registration *factoryRegistration,
) {
	service.mutex.Lock()
	if service.factory == registration {
		service.factory = nil
	}
	service.mutex.Unlock()
}

func (service *RegistryService) registeredFactory() (Factory, error) {
	service.mutex.RLock()
	registration := service.factory
	service.mutex.RUnlock()
	if registration == nil {
		return nil, errors.New(
			"agent: no Agent factory attached; mount an Agent Loop Plugin",
		)
	}
	return registration.factory, nil
}

// Create constructs, publishes, and commits one fresh exact Agent epoch.
func (service *RegistryService) Create(
	requestContext context.Context,
	settings CreateOptions,
) (Handle, error) {
	if requestContext == nil {
		return Handle{}, errors.New("agent: Create Context is nil")
	}
	agentFactory, err := service.registeredFactory()
	if err != nil {
		return Handle{}, err
	}
	pending, err := service.coordinator.reserve(
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
	agentFactory, err := service.registeredFactory()
	if err != nil {
		return Handle{}, err
	}
	pending, err := service.coordinator.reserve(
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
	return service.coordinator.closeAll(closeContext)
}

func (service *RegistryService) assertClosed() error {
	return service.coordinator.assertClosed()
}

var _ Registry = (*RegistryService)(nil)
var _ Constructor = (*RegistryService)(nil)
var _ ScopeProvisioning = (*RegistryService)(nil)
var _ DescendantLifecycle = (*RegistryService)(nil)
var _ RuntimeLifecycle = (*RegistryService)(nil)
