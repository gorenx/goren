package agent

import (
	"errors"
	"sort"

	"github.com/gorenx/goren/session"
)

// ErrClosing means an Agent or its ancestor no longer accepts new business.
var ErrClosing = errors.New("agent: Agent or ancestor is closing")

// Registration records one Agent identity and its immutable runtime parent.
// It contains no reference to a live Agent or framework activation.
type Registration struct {
	ID       session.SessionID
	ParentID *session.SessionID
}

// Registry owns Agent membership and parent-child constraints. Its actor owner
// serializes operations and performs creation and shutdown outside this object.
type Registry struct {
	// entries maps Agent IDs to their registered identity and optional parent ID.
	// Membership does not imply that component initialization has completed.
	entries map[session.SessionID]Registration
	// closing is a set of registered Agent IDs whose close was admitted.
	// Presence forbids new business in that subtree, not completion replies.
	// It says nothing about actor activation or resource shutdown progress.
	closing map[session.SessionID]struct{}
}

// Register adds a fresh identity under an already registered parent, if any.
func (directory *Registry) Register(entry Registration) error {
	if entry.ID == "" {
		return errors.New("agent: registration ID is required")
	}
	if _, found := directory.entries[entry.ID]; found {
		return errors.New("agent: identity is already registered")
	}
	if entry.ParentID != nil {
		if *entry.ParentID == entry.ID {
			return errors.New("agent: parent cannot be self")
		}
		if err := directory.CheckAdmission(*entry.ParentID); err != nil {
			return err
		}
	}
	if directory.entries == nil {
		directory.entries = make(map[session.SessionID]Registration)
	}
	directory.entries[entry.ID] = cloneRegistration(entry)
	return nil
}

// Get returns a detached registration when the identity is present.
func (directory *Registry) Get(identifier session.SessionID) (Registration, bool) {
	entry, found := directory.entries[identifier]
	return cloneRegistration(entry), found
}

// List returns detached registrations ordered by Agent ID.
func (directory *Registry) List() []Registration {
	result := make([]Registration, 0, len(directory.entries))
	for _, entry := range directory.entries {
		result = append(result, cloneRegistration(entry))
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

// Children returns the directly registered children in stable ID order.
func (directory *Registry) Children(identifier session.SessionID) []session.SessionID {
	result := make([]session.SessionID, 0)
	for _, entry := range directory.entries {
		if entry.ParentID != nil && *entry.ParentID == identifier {
			result = append(result, entry.ID)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

// Remove requires children to have completed their independent removal first.
// Removing an absent identity is idempotent.
func (directory *Registry) Remove(identifier session.SessionID) error {
	if len(directory.Children(identifier)) != 0 {
		return errors.New("agent: registered children remain")
	}
	delete(directory.entries, identifier)
	delete(directory.closing, identifier)
	return nil
}

// BeginClose stops new admission beneath an existing identity without removing
// any child or running resource cleanup. Repeated calls are idempotent.
func (directory *Registry) BeginClose(identifier session.SessionID) error {
	if _, found := directory.entries[identifier]; !found {
		return errors.New("agent: identity is not registered")
	}
	if directory.closing == nil {
		directory.closing = make(map[session.SessionID]struct{})
	}
	directory.closing[identifier] = struct{}{}
	return nil
}

// CheckAdmission rejects missing identities and closing ancestors. It does not
// establish readiness or authorize a particular framework creation.
func (directory *Registry) CheckAdmission(identifier session.SessionID) error {
	for {
		entry, found := directory.entries[identifier]
		if !found {
			return errors.New("agent: identity is not registered")
		}
		if _, stopped := directory.closing[identifier]; stopped {
			return ErrClosing
		}
		if entry.ParentID == nil {
			return nil
		}
		identifier = *entry.ParentID
	}
}

func cloneRegistration(entry Registration) Registration {
	if entry.ParentID != nil {
		identifier := *entry.ParentID
		entry.ParentID = &identifier
	}
	return entry
}
