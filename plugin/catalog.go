package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Factory strictly decodes owner-defined configuration and constructs one plugin.
type Factory[C any] interface {
	Name() string
	DecodeConfig(json.RawMessage) (C, error)
	New(context.Context, C) (Plugin, error)
}

type factoryEntry interface {
	create(context.Context, json.RawMessage) (Plugin, error)
}

type typedFactory[C any] struct {
	implementation Factory[C]
}

func (adapter typedFactory[C]) create(requestContext context.Context, rawConfig json.RawMessage) (Plugin, error) {
	typedConfig, err := adapter.implementation.DecodeConfig(rawConfig)
	if err != nil {
		return nil, err
	}
	return adapter.implementation.New(requestContext, typedConfig)
}

// Catalog stores statically linked factories by canonical source plugin name.
type Catalog struct {
	mu      sync.RWMutex
	entries map[string]factoryEntry
}

// NewCatalog creates an empty factory catalog.
func NewCatalog() *Catalog {
	return &Catalog{entries: make(map[string]factoryEntry)}
}

// RegisterFactory adds one typed factory while keeping type erasure inside the catalog.
func RegisterFactory[C any](registry *Catalog, implementation Factory[C]) error {
	if registry == nil {
		return errors.New("plugin: factory catalog is nil")
	}
	if implementation == nil {
		return errors.New("plugin: factory is nil")
	}
	canonicalName := implementation.Name()
	if strings.TrimSpace(canonicalName) == "" || canonicalName != strings.TrimSpace(canonicalName) {
		return errors.New("plugin: factory name must be non-empty and trimmed")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.entries[canonicalName]; exists {
		return fmt.Errorf("plugin: factory %q is already registered", canonicalName)
	}
	registry.entries[canonicalName] = typedFactory[C]{implementation: implementation}
	return nil
}

// Create performs the registered strict decode and typed construction pipeline.
func (registry *Catalog) Create(requestContext context.Context, factoryName string, rawConfig json.RawMessage) (Plugin, error) {
	registry.mu.RLock()
	entry := registry.entries[factoryName]
	registry.mu.RUnlock()
	if entry == nil {
		return nil, fmt.Errorf("plugin: factory %q is not registered", factoryName)
	}
	return entry.create(requestContext, rawConfig)
}

// Names returns registered factory names in deterministic order.
func (registry *Catalog) Names() []string {
	registry.mu.RLock()
	factoryNames := make([]string, 0, len(registry.entries))
	for factoryName := range registry.entries {
		factoryNames = append(factoryNames, factoryName)
	}
	registry.mu.RUnlock()
	sort.Strings(factoryNames)
	return factoryNames
}
