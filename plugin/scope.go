package plugin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

type serviceContribution struct {
	ref   ServiceRef
	value any
	owned bool
}

// eventListenerState retains listener ownership and routing metadata. Its
// ordinal is the Runtime-wide registration sequence used to recover a
// deterministic order after collecting listeners from multiple plugin
// Scopes; it is neither a Session event seq nor a priority.
type eventListenerState struct {
	ref     eventRef
	owned   bool
	ordinal uint64
	target  ScopeKey
}

type eventSubscription interface {
	listenerState() *eventListenerState
}

type typedEventSubscription[P, R any, H EventHandler[P, R]] struct {
	metadata eventListenerState
	callback H
}

func (listener *typedEventSubscription[P, R, H]) listenerState() *eventListenerState {
	return &listener.metadata
}

type ownedEffect struct {
	label    string
	release  Disposer
	released bool
}

// Scope owns all contributions and cleanup effects created by one Apply call.
// A scope is private until Runtime atomically activates it.
type Scope struct {
	owner         *Runtime
	record        *pluginRecord
	mu            sync.Mutex
	services      map[string]*serviceContribution
	subscriptions []eventSubscription
	effects       []*ownedEffect
	closed        bool
	activated     bool
	disposing     bool
	parent        *Scope
	target        ScopeKey
	children      map[string]*Scope
}

func newScope(owner *Runtime, record *pluginRecord) *Scope {
	return &Scope{
		owner:    owner,
		record:   record,
		services: make(map[string]*serviceContribution),
		children: make(map[string]*Scope),
	}
}

// Target returns the child-scope identity used by scoped capability
// registries. Root plugin scopes return the global zero key.
func (pluginScope *Scope) Target() ScopeKey {
	if pluginScope == nil {
		return ScopeKey{}
	}
	return pluginScope.target
}

// Child creates an effect-owned child Scope with a new opaque identity. The
// returned disposer may end it early; parent teardown also disposes it.
func (pluginScope *Scope) Child(label string) (*Scope, Disposer, error) {
	if pluginScope == nil {
		return nil, nil, errors.New("plugin: create child from nil scope")
	}
	if strings.TrimSpace(label) == "" || label != strings.TrimSpace(label) {
		return nil, nil, errors.New("plugin: child scope label must be non-empty and trimmed")
	}
	pluginScope.mu.Lock()
	if pluginScope.closed {
		pluginScope.mu.Unlock()
		return nil, nil, errors.New("plugin: scope is closed")
	}
	if _, exists := pluginScope.children[label]; exists {
		pluginScope.mu.Unlock()
		return nil, nil, fmt.Errorf("plugin: child scope %q already exists", label)
	}
	descendant := &Scope{
		owner: pluginScope.owner, record: pluginScope.record,
		services: make(map[string]*serviceContribution), children: make(map[string]*Scope),
		parent: pluginScope, target: ScopeKey{token: &scopeToken{parent: pluginScope.target.token}},
		activated: pluginScope.activated,
	}
	pluginScope.children[label] = descendant
	pluginScope.mu.Unlock()

	releaseChild := func(closeContext context.Context) error {
		cleanupErr := descendant.dispose(closeContext)
		pluginScope.mu.Lock()
		if pluginScope.children[label] == descendant {
			delete(pluginScope.children, label)
		}
		pluginScope.mu.Unlock()
		return cleanupErr
	}
	ownedRelease, err := pluginScope.own("scope:"+label, releaseChild)
	if err != nil {
		return nil, nil, errors.Join(err, releaseChild(context.Background()))
	}
	return descendant, ownedRelease, nil
}

// Own records an already-acquired resource in a Scope and returns its
// idempotent early-release capability.
func Own(pluginScope *Scope, label string, release Disposer) (Disposer, error) {
	if pluginScope == nil {
		return nil, errors.New("plugin: own effect on nil scope")
	}
	return pluginScope.own(label, release)
}

// Effect acquires a resource immediately and records its disposer in this
// scope. If setup fails, no empty effect is retained.
func (pluginScope *Scope) Effect(
	requestContext context.Context,
	label string,
	setup func(context.Context) (Disposer, error),
) error {
	if setup == nil {
		return errors.New("plugin: effect setup is nil")
	}
	release, err := setup(requestContext)
	if err != nil {
		return err
	}
	if release == nil {
		return errors.New("plugin: effect setup returned a nil disposer")
	}
	_, err = pluginScope.own(label, release)
	if err != nil {
		return errors.Join(err, release(requestContext))
	}
	return nil
}

func (pluginScope *Scope) own(label string, release Disposer) (Disposer, error) {
	if release == nil {
		return nil, errors.New("plugin: disposer is nil")
	}
	pluginScope.mu.Lock()
	defer pluginScope.mu.Unlock()
	if pluginScope.closed {
		return nil, errors.New("plugin: scope is closed")
	}
	effectEntry := &ownedEffect{label: label, release: release}
	pluginScope.effects = append(pluginScope.effects, effectEntry)
	return func(closeContext context.Context) error {
		pluginScope.mu.Lock()
		if effectEntry.released {
			pluginScope.mu.Unlock()
			return nil
		}
		effectEntry.released = true
		pluginScope.mu.Unlock()
		return effectEntry.release(closeContext)
	}, nil
}

func (pluginScope *Scope) provide(definition ServiceRef, value any) (Disposer, error) {
	if err := definition.validate(); err != nil {
		return nil, err
	}
	if pluginScope.parent != nil {
		return nil, errors.New("plugin: child scopes cannot provide root services")
	}
	if !containsRef(pluginScope.record.metadata.Provides, definition) {
		return nil, fmt.Errorf("plugin: %s did not declare provided service %q", pluginScope.record.metadata.Name, definition.name)
	}
	pluginScope.mu.Lock()
	if pluginScope.closed {
		pluginScope.mu.Unlock()
		return nil, errors.New("plugin: scope is closed")
	}
	if _, exists := pluginScope.services[definition.name]; exists {
		pluginScope.mu.Unlock()
		return nil, fmt.Errorf("plugin: %s provided service %q twice", pluginScope.record.metadata.Name, definition.name)
	}
	contribution := &serviceContribution{ref: definition, value: value, owned: true}
	pluginScope.services[definition.name] = contribution
	pluginScope.mu.Unlock()

	release, err := pluginScope.own(
		"provide:"+definition.name,
		func(disposeContext context.Context) error {
			pluginScope.mu.Lock()
			withdrawLive := pluginScope.activated && !pluginScope.disposing
			if contribution.owned {
				delete(pluginScope.services, definition.name)
				contribution.owned = false
			}
			pluginScope.mu.Unlock()
			if withdrawLive {
				return pluginScope.owner.withdrawService(disposeContext, pluginScope.record, definition, contribution)
			}
			return nil
		},
	)
	if err != nil {
		pluginScope.mu.Lock()
		delete(pluginScope.services, definition.name)
		pluginScope.mu.Unlock()
		return nil, err
	}
	pluginScope.mu.Lock()
	publishLive := pluginScope.activated && !pluginScope.disposing
	pluginScope.mu.Unlock()
	if publishLive {
		if err := pluginScope.owner.publishService(context.Background(), pluginScope.record, contribution); err != nil {
			return nil, errors.Join(err, release(context.Background()))
		}
	}
	return release, nil
}

func (pluginScope *Scope) require(definition ServiceRef) (any, bool) {
	if !containsRef(pluginScope.record.metadata.Requires, definition) && !containsRef(pluginScope.record.metadata.Optional, definition) {
		panic(fmt.Sprintf("plugin: %s did not declare service dependency %q", pluginScope.record.metadata.Name, definition.name))
	}
	return pluginScope.owner.resolveService(definition)
}

func (pluginScope *Scope) addSubscription(listener eventSubscription) (Disposer, error) {
	pluginScope.mu.Lock()
	if pluginScope.closed {
		pluginScope.mu.Unlock()
		return nil, errors.New("plugin: scope is closed")
	}
	listenerMetadata := listener.listenerState()
	listenerMetadata.owned = true
	listenerMetadata.target = pluginScope.target
	pluginScope.mu.Unlock()

	registryScope := pluginScope.root()
	registryScope.mu.Lock()
	registryScope.subscriptions = append(registryScope.subscriptions, listener)
	registryScope.mu.Unlock()
	release, err := pluginScope.own("listen:"+listenerMetadata.ref.name, func(context.Context) error {
		registryScope.mu.Lock()
		defer registryScope.mu.Unlock()
		listenerMetadata.owned = false
		registryScope.subscriptions = slices.DeleteFunc(registryScope.subscriptions, func(candidate eventSubscription) bool {
			return candidate == listener
		})
		return nil
	})
	if err != nil {
		registryScope.mu.Lock()
		listenerMetadata.owned = false
		registryScope.subscriptions = slices.DeleteFunc(registryScope.subscriptions, func(candidate eventSubscription) bool {
			return candidate == listener
		})
		registryScope.mu.Unlock()
		return nil, err
	}
	return release, nil
}

func (pluginScope *Scope) root() *Scope {
	rootScope := pluginScope
	for rootScope.parent != nil {
		rootScope = rootScope.parent
	}
	return rootScope
}

func (pluginScope *Scope) dispose(closeContext context.Context) error {
	pluginScope.mu.Lock()
	if pluginScope.closed {
		pluginScope.mu.Unlock()
		return nil
	}
	pluginScope.closed = true
	pluginScope.disposing = true
	pluginScope.activated = false
	effects := append([]*ownedEffect(nil), pluginScope.effects...)
	pluginScope.mu.Unlock()

	var cleanupErr error
	for index := len(effects) - 1; index >= 0; index-- {
		effectEntry := effects[index]
		pluginScope.mu.Lock()
		if effectEntry.released {
			pluginScope.mu.Unlock()
			continue
		}
		effectEntry.released = true
		pluginScope.mu.Unlock()
		cleanupErr = errors.Join(cleanupErr, effectEntry.release(closeContext))
	}
	return cleanupErr
}

func (pluginScope *Scope) effectLabels() []string {
	pluginScope.mu.Lock()
	defer pluginScope.mu.Unlock()
	labels := make([]string, 0, len(pluginScope.effects))
	for _, effectEntry := range pluginScope.effects {
		if !effectEntry.released {
			labels = append(labels, effectEntry.label)
		}
	}
	return labels
}

func containsRef(refs []ServiceRef, targetRef ServiceRef) bool {
	for _, definition := range refs {
		if definition.sameDefinition(targetRef) {
			return true
		}
	}
	return false
}
