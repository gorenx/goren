package commands

import (
	"errors"
	"fmt"
	"github.com/google/uuid"
	"sort"
	"strings"
)

// Registry owns global command metadata. Its actor serializes all access;
// handler addresses and admitted executions belong to the runtime adapter.
type Registry struct {
	entries map[string]Registration
}

// Registration identifies an exact definition without pointing back to Registry.
// Copies remain valid for already admitted calls after the definition is removed.
type Registration struct {
	ID         string
	Definition Definition
}

// Register validates and detaches a definition before publishing it.
func (owner *Registry) Register(specification Definition) (Registration, error) {
	if !commandNamePattern.MatchString(specification.Name) {
		return Registration{}, fmt.Errorf("commands: invalid name %q", specification.Name)
	}
	if strings.TrimSpace(specification.Description) == "" {
		return Registration{}, errors.New("commands: description is empty")
	}
	if specification.Input != nil {
		if strings.TrimSpace(specification.Input.Hint) == "" {
			return Registration{}, errors.New("commands: input hint is empty")
		}
		if specification.Input.Images {
			return Registration{}, errors.New("commands: image attachment admission is unavailable")
		}
	}
	if _, exists := owner.entries[specification.Name]; exists {
		return Registration{}, fmt.Errorf("commands: command %q is already registered", specification.Name)
	}
	entry := Registration{
		ID:         uuid.NewString(),
		Definition: cloneDefinition(specification),
	}
	if owner.entries == nil {
		owner.entries = make(map[string]Registration)
	}
	owner.entries[specification.Name] = entry
	return cloneRegistration(entry), nil
}

// Find returns the current detached definition and its registration identity.
func (owner *Registry) Find(name string) (Registration, bool) {
	entry, found := owner.entries[name]
	return cloneRegistration(entry), found
}

// Unregister removes only the identified definition. A stale handle cannot
// remove a later registration under the same command name.
func (owner *Registry) Unregister(handle Registration) bool {
	entry, found := owner.entries[handle.Definition.Name]
	if !found || entry.ID != handle.ID {
		return false
	}
	delete(owner.entries, handle.Definition.Name)
	return true
}

// List returns name-sorted, detached wire descriptors.
func (owner *Registry) List() []Descriptor {
	result := make([]Descriptor, 0, len(owner.entries))
	for _, entry := range owner.entries {
		result = append(result, Descriptor{
			Name:        entry.Definition.Name,
			Description: entry.Definition.Description,
			Input:       cloneInput(entry.Definition.Input),
		})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })
	return result
}

func cloneRegistration(original Registration) Registration {
	return Registration{
		ID:         original.ID,
		Definition: cloneDefinition(original.Definition),
	}
}
