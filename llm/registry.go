package llm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// APIAdapter streams one model-bound wire protocol into normalized LLM events.
type APIAdapter interface {
	API() API
	Stream(context.Context, Context, StreamOptions) (*EventStream, error)
}

// AdapterConstructor creates a model-bound adapter from generic model configuration.
type AdapterConstructor func(Model) (APIAdapter, error)

// Registry owns the active adapter constructor for each wire protocol.
type Registry struct {
	mu           sync.RWMutex
	constructors map[API]AdapterConstructor
}

// NewRegistry returns an empty API adapter registry.
func NewRegistry() *Registry {
	return &Registry{constructors: make(map[API]AdapterConstructor)}
}

// Register installs or replaces an adapter constructor for protocol.
func (adapterRegistry *Registry) Register(protocol API, construct AdapterConstructor) error {
	if protocol == "" {
		return errors.New("adapter API is required")
	}
	if construct == nil {
		return errors.New("adapter constructor is nil")
	}
	adapterRegistry.mu.Lock()
	adapterRegistry.constructors[protocol] = construct
	adapterRegistry.mu.Unlock()
	return nil
}

// Constructor returns the adapter constructor registered for protocol.
func (adapterRegistry *Registry) Constructor(protocol API) (AdapterConstructor, bool) {
	adapterRegistry.mu.RLock()
	construct, ok := adapterRegistry.constructors[protocol]
	adapterRegistry.mu.RUnlock()
	return construct, ok
}

// APIs returns registered protocols in stable lexical order.
func (adapterRegistry *Registry) APIs() []API {
	adapterRegistry.mu.RLock()
	protocols := make([]API, 0, len(adapterRegistry.constructors))
	for protocol := range adapterRegistry.constructors {
		protocols = append(protocols, protocol)
	}
	adapterRegistry.mu.RUnlock()
	sort.Slice(protocols, func(left, right int) bool { return protocols[left] < protocols[right] })
	return protocols
}

// Unregister removes the adapter constructor registered for protocol.
func (adapterRegistry *Registry) Unregister(protocol API) {
	adapterRegistry.mu.Lock()
	delete(adapterRegistry.constructors, protocol)
	adapterRegistry.mu.Unlock()
}

// Clear removes all registered adapter constructors.
func (adapterRegistry *Registry) Clear() {
	adapterRegistry.mu.Lock()
	clear(adapterRegistry.constructors)
	adapterRegistry.mu.Unlock()
}

func (adapterRegistry *Registry) resolve(protocol API) (AdapterConstructor, error) {
	construct, ok := adapterRegistry.Constructor(protocol)
	if !ok {
		return nil, fmt.Errorf("%w for API %q", ErrAdapterNotRegistered, protocol)
	}
	return construct, nil
}
