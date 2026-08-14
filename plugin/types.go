// Package plugin provides the service, event, and lifecycle runtime used to
// assemble Goren capabilities from statically linked Go plugins.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Disposer releases one effect owned by a plugin scope.
type Disposer func(context.Context) error

// Plugin contributes services, listeners, and other effects through a Scope.
type Plugin interface {
	Manifest() Manifest
	Apply(context.Context, *Scope) error
}

// Manifest declares a plugin's stable identity and service dependencies.
type Manifest struct {
	Name     string
	Provides []ServiceRef
	Requires []ServiceRef
	Optional []ServiceRef
}

// State is the externally observable lifecycle state of one plugin instance.
type State string

const (
	StateWaiting     State = "waiting-dependencies"
	StateStarting    State = "starting"
	StateActive      State = "active"
	StateStopping    State = "stopping"
	StateStopped     State = "stopped"
	StateRollingBack State = "rolling-back"
	StateFailed      State = "failed"
)

// PluginStatus is an immutable diagnostics view of one loaded plugin.
type PluginStatus struct {
	ID      uint64
	Name    string
	State   State
	Effects []string
	Error   error
}

// Handle identifies a plugin declaration for unload and replacement.
type Handle struct {
	owner *Runtime
	id    uint64
}

// ID returns the runtime-local plugin identifier.
func (pluginHandle Handle) ID() uint64 {
	return pluginHandle.id
}

func validateManifest(metadata Manifest) error {
	if strings.TrimSpace(metadata.Name) == "" || metadata.Name != strings.TrimSpace(metadata.Name) {
		return errors.New("plugin: manifest name must be non-empty and trimmed")
	}
	groups := []struct {
		label string
		refs  []ServiceRef
	}{
		{label: "provides", refs: metadata.Provides},
		{label: "requires", refs: metadata.Requires},
		{label: "optional", refs: metadata.Optional},
	}
	seen := make(map[string]string)
	for _, group := range groups {
		for _, definition := range group.refs {
			if err := definition.validate(); err != nil {
				return fmt.Errorf("plugin: %s %s: %w", metadata.Name, group.label, err)
			}
			if previous, exists := seen[definition.name]; exists {
				return fmt.Errorf("plugin: %s declares service %q in both %s and %s", metadata.Name, definition.name, previous, group.label)
			}
			seen[definition.name] = group.label
		}
	}
	return nil
}
