package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

type factorySlot struct {
	target Factory
}

type registryEntry struct {
	id      session.SessionID
	subject Agent
	owner   Agent

	mu              sync.Mutex
	announced       bool
	announcing      bool
	detachRequested bool
	entered         bool
}

type registryService struct {
	mu          sync.RWMutex
	entries     map[session.SessionID]*registryEntry
	order       []session.SessionID
	creator     *factorySlot
	sourceScope *plugin.Scope
	reporter    func(error)
}

// RegistryOptions supplies observer containment without changing lifecycle semantics.
type RegistryOptions struct {
	ObserverError func(error)
}

// NewRegistry creates an empty Agent service bound to its Provider Scope.
func NewRegistry(sourceScope *plugin.Scope, settings RegistryOptions) (Registry, error) {
	if sourceScope == nil {
		return nil, errors.New("agent: Registry source scope is nil")
	}
	reporter := settings.ObserverError
	if reporter == nil {
		reporter = func(error) {}
	}
	return &registryService{
		entries: make(map[session.SessionID]*registryEntry), sourceScope: sourceScope, reporter: reporter,
	}, nil
}

func (agents *registryService) SetFactory(
	requestContext context.Context,
	ownerScope *plugin.Scope,
	creator Factory,
) (plugin.Disposer, error) {
	if ownerScope == nil || creator == nil {
		return nil, errors.New("agent: factory owner scope and implementation are required")
	}
	slot := &factorySlot{target: creator}
	agents.mu.Lock()
	if agents.creator != nil {
		agents.mu.Unlock()
		return nil, errors.New("agent: an Agent factory is already registered")
	}
	agents.creator = slot
	agents.mu.Unlock()
	release, err := plugin.Own(ownerScope, "agents.setFactory()", func(context.Context) error {
		agents.mu.Lock()
		if agents.creator == slot {
			agents.creator = nil
		}
		agents.mu.Unlock()
		return nil
	})
	if err != nil {
		agents.mu.Lock()
		if agents.creator == slot {
			agents.creator = nil
		}
		agents.mu.Unlock()
		return nil, err
	}
	if err := requestContext.Err(); err != nil {
		return nil, errors.Join(err, release(context.Background()))
	}
	return release, nil
}

func (agents *registryService) Create(
	requestContext context.Context,
	ownerScope *plugin.Scope,
	settings CreateOptions,
) (Handle, error) {
	if ownerScope == nil {
		return Handle{}, errors.New("agent: create owner scope is nil")
	}
	agents.mu.RLock()
	slot := agents.creator
	agents.mu.RUnlock()
	if slot == nil {
		return Handle{}, errors.New("agent: no Agent factory registered; load an Agent Loop plugin")
	}
	return slot.target.CreateAgent(requestContext, ownerScope, settings)
}

func (agents *registryService) Register(
	requestContext context.Context,
	ownerScope *plugin.Scope,
	subject Agent,
	owner Agent,
) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("agent: register owner scope is nil")
	}
	releaseEntry, err := agents.Enter(subject, owner)
	if err != nil {
		return nil, err
	}
	releaseOwned, err := plugin.Own(ownerScope, "agents.register()", releaseEntry)
	if err != nil {
		return nil, errors.Join(err, releaseEntry(requestContext))
	}
	if err := agents.Announce(requestContext, subject); err != nil {
		return nil, errors.Join(err, releaseOwned(requestContext))
	}
	return releaseOwned, nil
}

func (agents *registryService) Enter(subject Agent, owner Agent) (plugin.Disposer, error) {
	if subject == nil || subject.SessionValue() == nil || subject.ScopeValue() == nil {
		return nil, errors.New("agent: Agent, Session, and Scope are required")
	}
	identifier := subject.ID()
	if identifier == "" {
		return nil, errors.New("agent: Agent id is empty")
	}
	if identifier != subject.SessionValue().ID() {
		return nil, fmt.Errorf("agent: Agent id %q does not match Session id %q", identifier, subject.SessionValue().ID())
	}
	if subject.ScopeValue().Target().IsGlobal() {
		return nil, errors.New("agent: Agent scope must be a child Scope")
	}
	if owner != nil && owner.ScopeValue() == nil {
		return nil, errors.New("agent: owning Agent scope is nil")
	}
	entry := &registryEntry{id: identifier, subject: subject, owner: owner, entered: true}
	agents.mu.Lock()
	if _, exists := agents.entries[identifier]; exists {
		agents.mu.Unlock()
		return nil, fmt.Errorf("agent: Agent %q is already registered", identifier)
	}
	agents.entries[identifier] = entry
	agents.order = append(agents.order, identifier)
	agents.mu.Unlock()

	return func(closeContext context.Context) error {
		entry.mu.Lock()
		if !entry.entered {
			entry.mu.Unlock()
			return nil
		}
		entry.entered = false
		if entry.announcing {
			entry.detachRequested = true
			entry.mu.Unlock()
			return nil
		}
		entry.mu.Unlock()
		return agents.detach(closeContext, entry)
	}, nil
}

func (agents *registryService) Announce(requestContext context.Context, subject Agent) error {
	if subject == nil {
		return errors.New("agent: announce subject is nil")
	}
	agents.mu.RLock()
	entry := agents.entries[subject.ID()]
	agents.mu.RUnlock()
	if entry == nil || entry.subject.ScopeValue() != subject.ScopeValue() || entry.subject.SessionValue() != subject.SessionValue() {
		return fmt.Errorf("agent: Agent %q is not live in this Registry", subject.ID())
	}
	entry.mu.Lock()
	if !entry.entered {
		entry.mu.Unlock()
		return fmt.Errorf("agent: Agent %q is no longer live in this Registry", subject.ID())
	}
	if entry.announced || entry.announcing {
		entry.mu.Unlock()
		return fmt.Errorf("agent: Agent %q was already announced", subject.ID())
	}
	entry.announced = true
	entry.announcing = true
	entry.mu.Unlock()

	dispatchErr := emitScoped(requestContext, agents.sourceScope, subject, createdEvent, LifecycleNotice{Subject: subject})
	entry.mu.Lock()
	entry.announcing = false
	detachRequested := entry.detachRequested
	entry.mu.Unlock()
	if detachRequested {
		dispatchErr = errors.Join(dispatchErr, agents.detach(requestContext, entry))
	}
	return dispatchErr
}

func (agents *registryService) detach(closeContext context.Context, entry *registryEntry) error {
	agents.mu.Lock()
	if agents.entries[entry.id] != entry {
		agents.mu.Unlock()
		return nil
	}
	delete(agents.entries, entry.id)
	agents.order = slices.DeleteFunc(agents.order, func(identifier session.SessionID) bool {
		return identifier == entry.id
	})
	agents.mu.Unlock()
	entry.mu.Lock()
	entry.detachRequested = false
	announced := entry.announced
	entry.mu.Unlock()
	if !announced {
		return nil
	}
	if err := emitScoped(closeContext, agents.sourceScope, entry.subject, disposedEvent, LifecycleNotice{Subject: entry.subject}); err != nil {
		agents.reporter(fmt.Errorf("agent: Agent %q disposed observers: %w", entry.id, err))
	}
	return nil
}

func (agents *registryService) Get(identifier session.SessionID) (Agent, bool) {
	agents.mu.RLock()
	entry := agents.entries[identifier]
	agents.mu.RUnlock()
	if entry == nil {
		return nil, false
	}
	return entry.subject, true
}

func (agents *registryService) IsOwnedBy(identifier session.SessionID, owner Agent) bool {
	if owner == nil {
		return false
	}
	agents.mu.RLock()
	entry := agents.entries[identifier]
	agents.mu.RUnlock()
	if entry == nil || entry.owner == nil {
		return false
	}
	if entry.owner.ScopeValue() == nil || owner.ScopeValue() == nil {
		return false
	}
	return entry.owner.ScopeValue().Target() == owner.ScopeValue().Target()
}

func (agents *registryService) List() []Agent {
	agents.mu.RLock()
	defer agents.mu.RUnlock()
	result := make([]Agent, 0, len(agents.order))
	for _, identifier := range agents.order {
		if entry := agents.entries[identifier]; entry != nil {
			result = append(result, entry.subject)
		}
	}
	return result
}

func (agents *registryService) Roots() []Agent {
	agents.mu.RLock()
	defer agents.mu.RUnlock()
	result := make([]Agent, 0, len(agents.order))
	for _, identifier := range agents.order {
		if entry := agents.entries[identifier]; entry != nil && entry.owner == nil {
			result = append(result, entry.subject)
		}
	}
	return result
}
