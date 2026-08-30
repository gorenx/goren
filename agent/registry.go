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
	mutex             sync.Mutex
	admission         registryAdmission
	factory           Factory
	byID              map[session.SessionID]*agentRecord
	records           []*agentRecord
	parentByChild     map[session.SessionID]session.SessionID
	childrenByParent  map[session.SessionID]map[session.SessionID]struct{}
	constructions     map[*agentRecord]struct{}
	reportObserverErr func(error)
	shutdown          lifecycleSignal
	shutdownErr       error
}

// NewRegistry constructs an inactive Agent Registry. RegistryPlugin binds its
// Factory when the module is activated.
func NewRegistry(registryConfig RegistryOptions) *RegistryService {
	reporter := registryConfig.ObserverError
	if reporter == nil {
		reporter = func(error) {}
	}
	return &RegistryService{
		admission:         registryAccepting,
		byID:              make(map[session.SessionID]*agentRecord),
		parentByChild:     make(map[session.SessionID]session.SessionID),
		childrenByParent:  make(map[session.SessionID]map[session.SessionID]struct{}),
		constructions:     make(map[*agentRecord]struct{}),
		reportObserverErr: reporter,
		shutdown:          newLifecycleSignal(),
	}
}

func (service *RegistryService) bind(agentFactory Factory) error {
	if agentFactory == nil {
		return errors.New("agent: Agent Factory is required")
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.admission != registryAccepting {
		return errors.New("agent: Agent Registry is shutting down")
	}
	if service.factory != nil {
		return errors.New("agent: Agent Registry is already active")
	}
	service.factory = agentFactory
	return nil
}

func (service *RegistryService) unbind() {
	service.mutex.Lock()
	service.factory = nil
	service.mutex.Unlock()
}

// Get returns the visible exact Agent registered for identifier.
func (service *RegistryService) Get(identifier session.SessionID) (Agent, bool) {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	record := service.byID[identifier]
	if !recordVisible(record) {
		return nil, false
	}
	return record.subject, true
}

// Contains reports whether subject is the visible exact Agent instance.
func (service *RegistryService) Contains(subject Agent) bool {
	if subject == nil {
		return false
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	record := service.byID[subject.ID()]
	return recordVisible(record) && Same(record.subject, subject)
}

// List returns visible exact Agents in construction order.
func (service *RegistryService) List() []Agent {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	result := make([]Agent, 0, len(service.records))
	for _, record := range service.records {
		if recordVisible(record) {
			result = append(result, record.subject)
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
	record, err := service.liveRecord(subject)
	if err != nil {
		return nil, err
	}
	return record.scope.ApplySetup(requestContext, subject, contribution)
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
	record, err := service.eventRecord(subject, fact)
	if err != nil {
		return err
	}
	return record.scope.Dispatch(requestContext, fact)
}

// eventRecord keeps only terminal event settlement available while Registry
// closes descendants. Scope shutdown remains the final event boundary.
func (service *RegistryService) eventRecord(
	subject Agent,
	fact AgentEvent,
) (*agentRecord, error) {
	if subject == nil {
		return nil, errors.New("agent: Agent subject is nil")
	}
	service.mutex.Lock()
	record := service.byID[subject.ID()]
	available := record != nil && record.scope != nil &&
		Same(record.subject, subject)
	if available && record.phase == recordClosing {
		_, available = fact.(AgentClosingEvent)
	} else {
		available = available && record.phase == recordLive
	}
	service.mutex.Unlock()
	if !available {
		return nil, errors.New("agent: exact Agent event Scope is unavailable")
	}
	return record, nil
}

// HasRuntimeDescendants reports whether the exact Agent owns active children.
func (service *RegistryService) HasRuntimeDescendants(subject Agent) bool {
	if subject == nil {
		return false
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	record := service.byID[subject.ID()]
	if record == nil || !Same(record.subject, subject) {
		return false
	}
	return len(service.childrenByParent[record.id]) != 0
}

func (service *RegistryService) liveRecord(subject Agent) (*agentRecord, error) {
	if subject == nil {
		return nil, errors.New("agent: Agent subject is nil")
	}
	service.mutex.Lock()
	record := service.byID[subject.ID()]
	live := record != nil && record.phase == recordLive &&
		Same(record.subject, subject)
	service.mutex.Unlock()
	if !live {
		return nil, errors.New("agent: exact Agent is not accepting Scope operations")
	}
	return record, nil
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
