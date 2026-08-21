// Package factory defines the statically linked Plugin construction boundary.
// It is deliberately outside the Plugin Runtime core package.
package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/gorenx/goren/plugin"
)

// Factory owns the named configuration, strict decoding, validation, and
// construction of one statically linked Plugin. Raw configuration ends at this
// boundary and must never reach Plugin business logic or Runtime.
type Factory interface {
	Name() string
	Create(createContext context.Context, rawConfig json.RawMessage) (plugin.Plugin, error)
}

// Catalog is a thread-safe directory of statically linked Factories. It owns
// name uniqueness and lookup only; it does not construct or mount Plugins.
type Catalog struct {
	mu      sync.RWMutex
	entries map[string]Factory
}

// NewCatalog creates an empty Factory directory.
func NewCatalog() *Catalog {
	return &Catalog{
		entries: make(map[string]Factory),
	}
}

// Register adds one statically linked Factory.
func (directory *Catalog) Register(candidate Factory) error {
	if directory == nil {
		return errors.New("plugin factory: catalog is nil")
	}
	if candidate == nil {
		return errors.New("plugin factory: factory is nil")
	}
	canonicalName := candidate.Name()
	if strings.TrimSpace(canonicalName) == "" || canonicalName != strings.TrimSpace(canonicalName) {
		return errors.New("plugin factory: factory name must be non-empty and trimmed")
	}
	directory.mu.Lock()
	defer directory.mu.Unlock()
	if _, exists := directory.entries[canonicalName]; exists {
		return fmt.Errorf("plugin factory: factory %q is already registered", canonicalName)
	}
	directory.entries[canonicalName] = candidate
	return nil
}

// Lookup returns one Factory without constructing a Plugin.
func (directory *Catalog) Lookup(canonicalName string) (Factory, error) {
	if directory == nil {
		return nil, errors.New("plugin factory: catalog is nil")
	}
	directory.mu.RLock()
	candidate := directory.entries[canonicalName]
	directory.mu.RUnlock()
	if candidate == nil {
		return nil, fmt.Errorf("plugin factory: factory %q is not registered", canonicalName)
	}
	return candidate, nil
}

// Names returns registered Factory names in deterministic order.
func (directory *Catalog) Names() []string {
	if directory == nil {
		return nil
	}
	directory.mu.RLock()
	canonicalNames := make([]string, 0, len(directory.entries))
	for canonicalName := range directory.entries {
		canonicalNames = append(canonicalNames, canonicalName)
	}
	directory.mu.RUnlock()
	sort.Strings(canonicalNames)
	return canonicalNames
}
