package plugin

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type serviceContribution struct {
	ref   ServiceRef
	value any
	owned bool
}

type eventSubscription struct {
	ref     eventRef
	invoke  any
	owned   bool
	ordinal uint64
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
	subscriptions []*eventSubscription
	effects       []*ownedEffect
	closed        bool
}

func newScope(owner *Runtime, record *pluginRecord) *Scope {
	return &Scope{owner: owner, record: record, services: make(map[string]*serviceContribution)}
}

// Effect acquires a resource immediately and records its disposer in this
// scope. If setup fails, no empty effect is retained.
func (pluginScope *Scope) Effect(requestContext context.Context, label string, setup func(context.Context) (Disposer, error)) error {
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

	release, err := pluginScope.own("provide:"+definition.name, func(context.Context) error {
		pluginScope.mu.Lock()
		defer pluginScope.mu.Unlock()
		if contribution.owned {
			delete(pluginScope.services, definition.name)
			contribution.owned = false
		}
		return nil
	})
	if err != nil {
		pluginScope.mu.Lock()
		delete(pluginScope.services, definition.name)
		pluginScope.mu.Unlock()
		return nil, err
	}
	return release, nil
}

func (pluginScope *Scope) require(definition ServiceRef) (any, bool) {
	if !containsRef(pluginScope.record.metadata.Requires, definition) && !containsRef(pluginScope.record.metadata.Optional, definition) {
		panic(fmt.Sprintf("plugin: %s did not declare service dependency %q", pluginScope.record.metadata.Name, definition.name))
	}
	return pluginScope.owner.resolveService(definition)
}

func (pluginScope *Scope) addSubscription(subscription *eventSubscription) (Disposer, error) {
	pluginScope.mu.Lock()
	if pluginScope.closed {
		pluginScope.mu.Unlock()
		return nil, errors.New("plugin: scope is closed")
	}
	subscription.owned = true
	pluginScope.subscriptions = append(pluginScope.subscriptions, subscription)
	pluginScope.mu.Unlock()
	return pluginScope.own("listen:"+subscription.ref.name, func(context.Context) error {
		pluginScope.mu.Lock()
		defer pluginScope.mu.Unlock()
		subscription.owned = false
		return nil
	})
}

func (pluginScope *Scope) dispose(closeContext context.Context) error {
	pluginScope.mu.Lock()
	if pluginScope.closed {
		pluginScope.mu.Unlock()
		return nil
	}
	pluginScope.closed = true
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
