package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/session"
)

// RegistryOptions supplies observer containment without changing lifecycle
// semantics.
type RegistryOptions struct {
	ObserverError func(error)
}

// Registry is the read-only view of currently visible exact Agent instances.
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

// ScopeSetup applies one Setup to an exact live Agent Scope.
type ScopeSetup interface {
	ApplySetup(context.Context, Agent, Setup) (ScopeResources, error)
}

// EventDispatcher publishes a producer-owned event through one exact Agent
// Scope. During closing, only AgentClosingEvent facts remain admissible.
type EventDispatcher interface {
	Dispatch(context.Context, Agent, AgentEvent) error
}

// RuntimeDescendants observes managed runtime descendants of an exact Agent.
type RuntimeDescendants interface {
	HasRuntimeDescendants(Agent) bool
}

// RegistryService owns Agent membership, parent-child relations, and
// multi-Agent lifecycle. AgentLoop remains responsible for each Host only.
type RegistryService struct {
	mutex            sync.Mutex
	admission        registryAdmission
	factory          Factory
	byID             map[session.SessionID]*agentLifetime
	lifetimes        []*agentLifetime
	parentByChild    map[session.SessionID]session.SessionID
	childrenByParent map[session.SessionID]map[session.SessionID]struct{}
	// Key is an exact Agent lifetime whose Factory call was admitted. The empty
	// value carries no ownership; membership is the construction join set.
	constructions     map[*agentLifetime]struct{}
	reportObserverErr func(error)
	deactivation      lifecycleSignal
	deactivationErr   error
}

// NewRegistry constructs an inactive Agent Registry. RegistryPlugin binds its
// Factory when the module is activated.
func NewRegistry(registryConfig RegistryOptions) *RegistryService {
	reporter := registryConfig.ObserverError
	if reporter == nil {
		reporter = func(error) {}
	}
	return &RegistryService{
		admission:         registryInactive,
		byID:              make(map[session.SessionID]*agentLifetime),
		parentByChild:     make(map[session.SessionID]session.SessionID),
		childrenByParent:  make(map[session.SessionID]map[session.SessionID]struct{}),
		constructions:     make(map[*agentLifetime]struct{}),
		reportObserverErr: reporter,
		deactivation:      newLifecycleSignal(),
	}
}

func (service *RegistryService) bind(agentFactory Factory) error {
	if agentFactory == nil {
		return errors.New("agent: Agent Factory is required")
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.admission != registryInactive {
		return errors.New("agent: Agent Registry is already active")
	}
	if len(service.lifetimes) != 0 || len(service.constructions) != 0 {
		return errors.New("agent: Agent Registry retained inactive lifetimes")
	}
	service.factory = agentFactory
	service.admission = registryAccepting
	service.deactivation = newLifecycleSignal()
	service.deactivationErr = nil
	return nil
}

// Get returns the visible exact Agent registered for identifier.
func (service *RegistryService) Get(identifier session.SessionID) (Agent, bool) {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	lifetime := service.byID[identifier]
	if lifetime == nil || !lifetime.Visible() {
		return nil, false
	}
	return lifetime.Agent(), true
}

// Contains reports whether subject is the visible exact Agent instance.
func (service *RegistryService) Contains(subject Agent) bool {
	if subject == nil {
		return false
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	lifetime := service.byID[subject.ID()]
	return lifetime != nil && lifetime.Visible() && lifetime.Matches(subject)
}

// List returns visible exact Agents in construction order.
func (service *RegistryService) List() []Agent {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	result := make([]Agent, 0, len(service.lifetimes))
	for _, lifetime := range service.lifetimes {
		if lifetime.Visible() {
			result = append(result, lifetime.Agent())
		}
	}
	return result
}

// ApplySetup applies one cohesive Setup to the exact live Agent Scope.
func (service *RegistryService) ApplySetup(
	requestContext context.Context,
	subject Agent,
	contribution Setup,
) (ScopeResources, error) {
	if contribution == nil {
		return nil, errors.New("agent: Agent Setup is nil")
	}
	lifetime, err := service.liveLifetime(subject)
	if err != nil {
		return nil, err
	}
	return lifetime.ApplySetup(requestContext, contribution)
}

// Dispatch publishes fact through the Scope of the exact live Agent.
func (service *RegistryService) Dispatch(
	requestContext context.Context,
	subject Agent,
	fact AgentEvent,
) error {
	if fact == nil {
		return errors.New("agent: AgentEvent is nil")
	}
	lifetime, err := service.exactLifetime(subject)
	if err != nil {
		return err
	}
	if requestContext == nil {
		return errors.New("agent: Agent event Context is nil")
	}
	return lifetime.DispatchEvent(requestContext, subject, fact)
}

func (service *RegistryService) exactLifetime(
	subject Agent,
) (*agentLifetime, error) {
	if subject == nil {
		return nil, errors.New("agent: Agent subject is nil")
	}
	service.mutex.Lock()
	lifetime := service.byID[subject.ID()]
	available := lifetime != nil && lifetime.Matches(subject)
	service.mutex.Unlock()
	if !available {
		return nil, errors.New("agent: exact Agent event Scope is unavailable")
	}
	return lifetime, nil
}

// HasRuntimeDescendants reports whether the exact Agent owns active children.
func (service *RegistryService) HasRuntimeDescendants(subject Agent) bool {
	if subject == nil {
		return false
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	lifetime := service.byID[subject.ID()]
	if lifetime == nil || !lifetime.Matches(subject) {
		return false
	}
	return len(service.childrenByParent[lifetime.SessionID()]) != 0
}

func (service *RegistryService) liveLifetime(subject Agent) (*agentLifetime, error) {
	if subject == nil {
		return nil, errors.New("agent: Agent subject is nil")
	}
	service.mutex.Lock()
	lifetime := service.byID[subject.ID()]
	live := lifetime != nil && lifetime.AcceptsSetup() &&
		lifetime.Matches(subject)
	service.mutex.Unlock()
	if !live {
		return nil, errors.New("agent: exact Agent is not accepting Scope operations")
	}
	return lifetime, nil
}

func (service *RegistryService) reportObserverFailure(problem error) {
	if problem == nil {
		return
	}
	defer func() { _ = recover() }()
	service.reportObserverErr(problem)
}

var _ Registry = (*RegistryService)(nil)
var _ Constructor = (*RegistryService)(nil)
var _ ScopeSetup = (*RegistryService)(nil)
var _ EventDispatcher = (*RegistryService)(nil)
var _ RuntimeDescendants = (*RegistryService)(nil)
