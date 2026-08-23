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

type registryEntry struct {
	id      session.SessionID
	subject Agent
	owner   Agent

	mutex         sync.Mutex
	announced     bool
	announcing    bool
	removePending bool
	entered       bool
}

// RegistryOptions supplies observer containment without changing lifecycle
// semantics.
type RegistryOptions struct {
	ObserverError func(error)
}

// Registry tracks live Agent membership and delegates construction to the
// currently attached Agent Loop Factory.
type Registry interface {
	plugin.Service
	RegisterFactory(Factory) (FactoryRegistration, error)
	Create(context.Context, CreateOptions) (Handle, error)
	Resume(context.Context, ResumeOptions) (Handle, error)
	Enter(Agent, Agent) error
	Announce(context.Context, Agent) error
	Remove(context.Context, Agent) error
	Get(session.SessionID) (Agent, bool)
	Contains(Agent) bool
	IsOwnedBy(session.SessionID, Agent) bool
	List() []Agent
	Roots() []Agent
}

// FactoryRegistration owns one exact Factory registration.
type FactoryRegistration interface {
	Unregister()
}

type factoryRegistration struct {
	agents  *RegistryPlugin
	factory Factory
	once    sync.Once
}

func (registration *factoryRegistration) Unregister() {
	if registration == nil {
		return
	}
	registration.once.Do(func() {
		registration.agents.unregisterFactory(registration)
	})
}

// RegistryPlugin is the canonical Agent Registry Service Plugin.
type RegistryPlugin struct {
	plugin.Base
	mutex    sync.RWMutex
	entries  map[session.SessionID]*registryEntry
	order    []session.SessionID
	factory  *factoryRegistration
	reporter func(error)
}

// NewRegistry constructs an empty Agent Registry Plugin.
func NewRegistry(settings RegistryOptions) *RegistryPlugin {
	reporter := settings.ObserverError
	if reporter == nil {
		reporter = func(error) {}
	}
	return &RegistryPlugin{
		entries:  make(map[session.SessionID]*registryEntry),
		reporter: reporter,
	}
}

// Manifest provides the canonical Agent Registry Service.
func (owner *RegistryPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[Registry](owner),
		},
	}
}

// Apply validates startup cancellation before Registry publication.
func (*RegistryPlugin) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

// Dispose rejects dangling Agent membership after Agent Loop dependents stop.
func (agents *RegistryPlugin) Dispose(context.Context) error {
	agents.mutex.Lock()
	dangling := len(agents.entries)
	agents.entries = make(map[session.SessionID]*registryEntry)
	agents.order = nil
	agents.factory = nil
	agents.mutex.Unlock()
	if dangling != 0 {
		return fmt.Errorf(
			"agent: Registry stopped with %d live Agent lifecycle(s)",
			dangling,
		)
	}
	return nil
}

// RegisterFactory installs one exact Agent construction provider.
func (agents *RegistryPlugin) RegisterFactory(
	agentFactory Factory,
) (FactoryRegistration, error) {
	if agentFactory == nil {
		return nil, errors.New("agent: Agent factory is required")
	}
	agents.mutex.Lock()
	defer agents.mutex.Unlock()
	if agents.factory != nil {
		return nil, errors.New("agent: an Agent factory is already registered")
	}
	registration := &factoryRegistration{
		agents:  agents,
		factory: agentFactory,
	}
	agents.factory = registration
	return registration, nil
}

func (agents *RegistryPlugin) unregisterFactory(
	registration *factoryRegistration,
) {
	agents.mutex.Lock()
	if agents.factory == registration {
		agents.factory = nil
	}
	agents.mutex.Unlock()
}

// Create delegates fresh Agent construction to the attached Factory.
func (agents *RegistryPlugin) Create(
	requestContext context.Context,
	settings CreateOptions,
) (Handle, error) {
	agents.mutex.RLock()
	registration := agents.factory
	agents.mutex.RUnlock()
	if registration == nil {
		return Handle{}, errors.New(
			"agent: no Agent factory attached; mount an Agent Loop Plugin",
		)
	}
	return registration.factory.CreateAgent(
		requestContext,
		custodyFrom(requestContext),
		settings,
	)
}

// Resume delegates durable Agent restoration to the attached Factory.
func (agents *RegistryPlugin) Resume(
	requestContext context.Context,
	settings ResumeOptions,
) (Handle, error) {
	agents.mutex.RLock()
	registration := agents.factory
	agents.mutex.RUnlock()
	if registration == nil {
		return Handle{}, errors.New(
			"agent: no Agent factory attached; mount an Agent Loop Plugin",
		)
	}
	return registration.factory.ResumeAgent(
		requestContext,
		custodyFrom(requestContext),
		settings,
	)
}

// Enter reserves live membership without announcing creation.
func (agents *RegistryPlugin) Enter(subject Agent, owner Agent) error {
	if subject == nil || subject.RuntimePlugin() == nil ||
		subject.SessionValue() == nil {
		return errors.New("agent: Agent Plugin and Session are required")
	}
	identifier := subject.ID()
	if identifier == "" {
		return errors.New("agent: Agent id is empty")
	}
	if identifier != subject.SessionValue().ID() {
		return fmt.Errorf(
			"agent: Agent id %q does not match Session id %q",
			identifier,
			subject.SessionValue().ID(),
		)
	}
	if owner != nil && !agents.Contains(owner) {
		return errors.New("agent: owning Agent is not live in this Registry")
	}
	entry := &registryEntry{
		id:      identifier,
		subject: subject,
		owner:   owner,
		entered: true,
	}
	agents.mutex.Lock()
	if _, exists := agents.entries[identifier]; exists {
		agents.mutex.Unlock()
		return fmt.Errorf("agent: Agent %q is already registered", identifier)
	}
	agents.entries[identifier] = entry
	agents.order = append(agents.order, identifier)
	agents.mutex.Unlock()
	return nil
}

// Announce publishes the vetoable Agent creation edge exactly once.
func (agents *RegistryPlugin) Announce(
	requestContext context.Context,
	subject Agent,
) error {
	entry, err := agents.liveEntry(subject)
	if err != nil {
		return err
	}
	entry.mutex.Lock()
	if !entry.entered {
		entry.mutex.Unlock()
		return fmt.Errorf("agent: Agent %q is no longer live", subject.ID())
	}
	if entry.announced || entry.announcing {
		entry.mutex.Unlock()
		return fmt.Errorf("agent: Agent %q was already announced", subject.ID())
	}
	entry.announced = true
	entry.announcing = true
	entry.mutex.Unlock()

	dispatchErr := plugin.Publish(
		requestContext,
		subject,
		Created{
			Subject: subject,
		},
	)
	entry.mutex.Lock()
	entry.announcing = false
	removePending := entry.removePending
	entry.mutex.Unlock()
	if removePending {
		dispatchErr = errors.Join(
			dispatchErr,
			agents.removeEntry(requestContext, entry),
		)
	}
	return dispatchErr
}

// Remove deletes and announces only the exact live Agent instance.
func (agents *RegistryPlugin) Remove(
	requestContext context.Context,
	subject Agent,
) error {
	entry, err := agents.liveEntry(subject)
	if err != nil {
		return nil
	}
	entry.mutex.Lock()
	if !entry.entered {
		entry.mutex.Unlock()
		return nil
	}
	entry.entered = false
	if entry.announcing {
		entry.removePending = true
		entry.mutex.Unlock()
		return nil
	}
	entry.mutex.Unlock()
	return agents.removeEntry(requestContext, entry)
}

func (agents *RegistryPlugin) removeEntry(
	requestContext context.Context,
	entry *registryEntry,
) error {
	agents.mutex.Lock()
	if agents.entries[entry.id] != entry {
		agents.mutex.Unlock()
		return nil
	}
	delete(agents.entries, entry.id)
	agents.order = slices.DeleteFunc(
		agents.order,
		func(identifier session.SessionID) bool {
			return identifier == entry.id
		},
	)
	agents.mutex.Unlock()
	entry.mutex.Lock()
	entry.removePending = false
	announced := entry.announced
	entry.mutex.Unlock()
	if !announced {
		return nil
	}
	dispatchErr := plugin.Publish(
		requestContext,
		entry.subject,
		Disposed{
			Subject: entry.subject,
		},
	)
	if errors.Is(dispatchErr, plugin.ErrPluginNotActive) ||
		errors.Is(dispatchErr, plugin.ErrPluginNotBound) {
		dispatchErr = plugin.Publish(
			requestContext,
			agents,
			Disposed{
				Subject: entry.subject,
			},
		)
	}
	if dispatchErr != nil {
		agents.report(fmt.Errorf(
			"agent: Agent %q disposed observers: %w",
			entry.id,
			dispatchErr,
		))
	}
	return nil
}

func (agents *RegistryPlugin) liveEntry(subject Agent) (*registryEntry, error) {
	if subject == nil {
		return nil, errors.New("agent: Agent subject is nil")
	}
	agents.mutex.RLock()
	entry := agents.entries[subject.ID()]
	agents.mutex.RUnlock()
	if entry == nil || !Same(entry.subject, subject) {
		return nil, fmt.Errorf(
			"agent: Agent %q is not live in this Registry",
			subject.ID(),
		)
	}
	return entry, nil
}

// Get returns the live Agent registered for identifier.
func (agents *RegistryPlugin) Get(identifier session.SessionID) (Agent, bool) {
	agents.mutex.RLock()
	entry := agents.entries[identifier]
	agents.mutex.RUnlock()
	if entry == nil {
		return nil, false
	}
	return entry.subject, true
}

// Contains reports whether subject is the exact live Agent instance.
func (agents *RegistryPlugin) Contains(subject Agent) bool {
	if subject == nil {
		return false
	}
	agents.mutex.RLock()
	entry := agents.entries[subject.ID()]
	agents.mutex.RUnlock()
	return entry != nil && Same(entry.subject, subject)
}

// IsOwnedBy reports exact live runtime ownership captured at Enter.
func (agents *RegistryPlugin) IsOwnedBy(
	identifier session.SessionID,
	owner Agent,
) bool {
	if owner == nil {
		return false
	}
	agents.mutex.RLock()
	entry := agents.entries[identifier]
	agents.mutex.RUnlock()
	return entry != nil && Same(entry.owner, owner)
}

// List returns live Agents in registration order.
func (agents *RegistryPlugin) List() []Agent {
	agents.mutex.RLock()
	defer agents.mutex.RUnlock()
	result := make([]Agent, 0, len(agents.order))
	for _, identifier := range agents.order {
		if entry := agents.entries[identifier]; entry != nil {
			result = append(result, entry.subject)
		}
	}
	return result
}

// Roots returns live Agents without an owning Agent.
func (agents *RegistryPlugin) Roots() []Agent {
	agents.mutex.RLock()
	defer agents.mutex.RUnlock()
	result := make([]Agent, 0, len(agents.order))
	for _, identifier := range agents.order {
		if entry := agents.entries[identifier]; entry != nil && entry.owner == nil {
			result = append(result, entry.subject)
		}
	}
	return result
}

func (agents *RegistryPlugin) report(problem error) {
	defer func() { _ = recover() }()
	agents.reporter(problem)
}
