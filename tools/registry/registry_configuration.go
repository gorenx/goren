package toolregistry

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/tool"
)

// Tool describes a configured tool, not a constructed implementation. Provider
// is borrowed and must remain immutable for the lifetime of an update.
type Tool struct {
	Name     string
	Provider tool.Provider
	Visible  bool
}

// Configuration fixes the resolved tools to one catalog update. It contains no
// implementation resources; callers open each Provider only inside their update barrier.
type Configuration struct {
	Update Update
	Tools  []Tool
}

// ConfigurationRegistered announces a retained configuration, not a published catalog.
type ConfigurationRegistered struct {
	ID      UpdateID
	AgentID session.SessionID
}

// ConfigurationTopic carries update identities for Runtime and Agent subscribers.
const ConfigurationTopic = "tools/configuration"

// InitializeDefaults records the startup defaults once, including an explicitly
// empty set. It never constructs tools or changes a committed Agent catalog.
func (owner *Registry) InitializeDefaults(entries []Tool) error {
	if owner.initialized {
		return errors.New("tools: default configuration already initialized")
	}
	err := validateTools(entries)
	if err != nil {
		return err
	}
	owner.defaults = slices.Clone(entries)
	owner.initialized = true
	return nil
}

// Configure captures complete Agent overrides over the startup defaults and
// reserves the catalog batch. Update IDs are unique; query an uncertain result
// with ReadConfiguration instead of submitting the ID again.
func (owner *Registry) Configure(identifier UpdateID, agentID session.SessionID, overrides []Tool) (ConfigurationRegistered, error) {
	if !owner.initialized {
		return ConfigurationRegistered{}, errors.New("tools: default configuration is not initialized")
	}
	if _, exists := owner.batches[identifier]; exists {
		return ConfigurationRegistered{}, errors.New("tools: configuration update ID already exists")
	}
	err := validateTools(overrides)
	if err != nil {
		return ConfigurationRegistered{}, err
	}
	resolved := slices.Clone(owner.defaults)
	positions := make(map[string]int, len(resolved))
	for index, entry := range resolved {
		positions[entry.Name] = index
	}
	for _, entry := range overrides {
		if index, found := positions[entry.Name]; found {
			resolved[index] = entry
		} else {
			resolved = append(resolved, entry)
		}
	}
	names := make([]string, len(resolved))
	for index, entry := range resolved {
		names[index] = entry.Name
	}
	declaration := Update{
		ID:      identifier,
		AgentID: agentID,
		Names:   names,
	}
	err = owner.Begin(declaration)
	if err != nil {
		return ConfigurationRegistered{}, err
	}
	owner.batches[identifier].configuration = &Configuration{
		Update: declaration,
		Tools:  resolved,
	}
	return ConfigurationRegistered{
		ID:      identifier,
		AgentID: agentID,
	}, nil
}

// ReadConfiguration returns the exact retained update, including before catalog
// publication. Missing configuration is distinct from an explicitly empty set.
func (owner *Registry) ReadConfiguration(identifier UpdateID) (Configuration, bool) {
	pending, exists := owner.batches[identifier]
	if !exists || pending.configuration == nil {
		return Configuration{}, false
	}
	detached := *pending.configuration
	detached.Update.Names = slices.Clone(detached.Update.Names)
	detached.Tools = slices.Clone(detached.Tools)
	return detached, true
}

func validateTools(entries []Tool) error {
	names := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.Name == "" ||
			strings.TrimSpace(entry.Name) != entry.Name ||
			entry.Name == tools.RunCodeName {
			return fmt.Errorf("tools: invalid configured tool name %q", entry.Name)
		}
		if entry.Provider == nil {
			return fmt.Errorf("tools: provider is required for %q", entry.Name)
		}
		if _, found := names[entry.Name]; found {
			return fmt.Errorf("tools: duplicate configuration for %q", entry.Name)
		}
		names[entry.Name] = struct{}{}
	}
	return nil
}
