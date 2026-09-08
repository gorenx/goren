package commands

import (
	"github.com/gorenx/goren/session"
	"sort"
)

// Catalog owns global defaults and Session overrides. Runtime addresses remain
// outside this business object. Callers serialize access.
type Catalog struct {
	defaults Registry
	sessions map[session.SessionID]*Registry
}

// Register publishes a global definition for an empty SessionID, or an override.
func (owner *Catalog) Register(id session.SessionID, input Definition) (Registration, error) {
	preview := Registry{}
	entry, err := preview.Register(input)
	if err != nil {
		return Registration{}, err
	}
	scope := &owner.defaults
	if id != "" {
		if owner.sessions == nil {
			owner.sessions = make(map[session.SessionID]*Registry)
		}
		scope = owner.sessions[id]
		if scope == nil {
			scope = &Registry{}
			owner.sessions[id] = scope
		}
	}
	if scope.entries == nil {
		scope.entries = make(map[string]Registration)
	}
	scope.entries[input.Name] = cloneRegistration(entry)
	return entry, nil
}

// Find resolves an override first and otherwise inherits the current default.
func (owner *Catalog) Find(id session.SessionID, name string) (Registration, bool) {
	if scope := owner.sessions[id]; scope != nil {
		if entry, found := scope.Find(name); found {
			return entry, true
		}
	}
	return owner.defaults.Find(name)
}

// Unregister removes an exact local registration without deleting inherited defaults.
func (owner *Catalog) Unregister(id session.SessionID, entry Registration) bool {
	if id == "" {
		return owner.defaults.Unregister(entry)
	}
	scope := owner.sessions[id]
	if scope == nil {
		return false
	}
	removed := scope.Unregister(entry)
	if len(scope.entries) == 0 {
		delete(owner.sessions, id)
	}
	return removed
}

// List merges current defaults and overrides into a detached name-sorted view.
func (owner *Catalog) List(id session.SessionID) []Descriptor {
	merged := make(map[string]Descriptor)
	for _, entry := range owner.defaults.List() {
		merged[entry.Name] = entry
	}
	if scope := owner.sessions[id]; scope != nil {
		for _, entry := range scope.List() {
			merged[entry.Name] = entry
		}
	}
	output := make([]Descriptor, 0, len(merged))
	for _, entry := range merged {
		output = append(output, entry)
	}
	sort.Slice(output, func(left, right int) bool { return output[left].Name < output[right].Name })
	return output
}
