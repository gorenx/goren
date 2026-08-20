// Package factory defines the statically linked Plugin construction boundary.
// It is deliberately outside the Plugin Runtime core package.
package factory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/plugin/configuration"
)

// Configurator strictly decodes and validates one owner-defined configuration
// Document, then returns an immutable configured Factory. It never mounts a
// Plugin or accesses Runtime.
type Configurator interface {
	Name() string
	Configure(document configuration.Document) (Factory, error)
}

// Factory constructs one Plugin from configuration and object dependencies it
// already owns. It never receives encoded configuration or accesses Runtime.
type Factory interface {
	Name() string
	Create(createContext context.Context) (plugin.Plugin, error)
}

// Catalog is a thread-safe directory of statically linked Configurators. It
// owns name uniqueness and lookup only; it does not configure, construct, or
// mount Plugins.
type Catalog struct {
	mu      sync.RWMutex
	entries map[string]Configurator
}

// NewCatalog creates an empty Configurator directory.
func NewCatalog() *Catalog {
	return &Catalog{
		entries: make(map[string]Configurator),
	}
}

// Register adds one statically linked Configurator.
func (directory *Catalog) Register(candidate Configurator) error {
	if directory == nil {
		return errors.New("plugin factory: catalog is nil")
	}
	if candidate == nil {
		return errors.New("plugin factory: configurator is nil")
	}
	canonicalName := candidate.Name()
	if strings.TrimSpace(canonicalName) == "" || canonicalName != strings.TrimSpace(canonicalName) {
		return errors.New("plugin factory: configurator name must be non-empty and trimmed")
	}
	directory.mu.Lock()
	defer directory.mu.Unlock()
	if _, exists := directory.entries[canonicalName]; exists {
		return fmt.Errorf("plugin factory: configurator %q is already registered", canonicalName)
	}
	directory.entries[canonicalName] = candidate
	return nil
}

// Lookup returns one Configurator without running configuration or Plugin
// construction.
func (directory *Catalog) Lookup(canonicalName string) (Configurator, error) {
	if directory == nil {
		return nil, errors.New("plugin factory: catalog is nil")
	}
	directory.mu.RLock()
	candidate := directory.entries[canonicalName]
	directory.mu.RUnlock()
	if candidate == nil {
		return nil, fmt.Errorf("plugin factory: configurator %q is not registered", canonicalName)
	}
	return candidate, nil
}

// Names returns registered Configurator names in deterministic order.
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
