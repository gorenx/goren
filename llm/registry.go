package llm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// APIAdapter streams one wire protocol into normalized goren events.
type APIAdapter interface {
	API() API
	Stream(context.Context, Model, Context, StreamOptions) (*EventStream, error)
}

type adapterRegistration struct {
	adapter  APIAdapter
	sourceID string
}

// Registry owns the active adapter for each wire protocol.
type Registry struct {
	mu       sync.RWMutex
	adapters map[API]adapterRegistration
}

// NewRegistry returns an empty API adapter registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[API]adapterRegistration)}
}

// Register installs or replaces the adapter for its wire protocol.
func (adapterRegistry *Registry) Register(protocolAdapter APIAdapter, sourceID string) error {
	if protocolAdapter == nil {
		return errors.New("API adapter is nil")
	}
	protocol := protocolAdapter.API()
	if protocol == "" {
		return errors.New("API adapter has no API")
	}
	adapterRegistry.mu.Lock()
	adapterRegistry.adapters[protocol] = adapterRegistration{adapter: protocolAdapter, sourceID: sourceID}
	adapterRegistry.mu.Unlock()
	return nil
}

// Adapter returns the adapter currently registered for api.
func (adapterRegistry *Registry) Adapter(protocol API) (APIAdapter, bool) {
	adapterRegistry.mu.RLock()
	registration, ok := adapterRegistry.adapters[protocol]
	adapterRegistry.mu.RUnlock()
	return registration.adapter, ok
}

// APIs returns registered protocols in stable lexical order.
func (adapterRegistry *Registry) APIs() []API {
	adapterRegistry.mu.RLock()
	protocols := make([]API, 0, len(adapterRegistry.adapters))
	for protocol := range adapterRegistry.adapters {
		protocols = append(protocols, protocol)
	}
	adapterRegistry.mu.RUnlock()
	sort.Slice(protocols, func(left, right int) bool { return protocols[left] < protocols[right] })
	return protocols
}

// UnregisterSource removes all adapters installed by one source.
func (adapterRegistry *Registry) UnregisterSource(sourceID string) {
	adapterRegistry.mu.Lock()
	for protocol, registration := range adapterRegistry.adapters {
		if registration.sourceID == sourceID {
			delete(adapterRegistry.adapters, protocol)
		}
	}
	adapterRegistry.mu.Unlock()
}

// Clear removes all registered adapters.
func (adapterRegistry *Registry) Clear() {
	adapterRegistry.mu.Lock()
	clear(adapterRegistry.adapters)
	adapterRegistry.mu.Unlock()
}

func (adapterRegistry *Registry) resolve(protocol API) (APIAdapter, error) {
	protocolAdapter, ok := adapterRegistry.Adapter(protocol)
	if !ok {
		return nil, fmt.Errorf("%w for API %q", ErrAdapterNotRegistered, protocol)
	}
	return protocolAdapter, nil
}
