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
	Role     Role
	ID       session.SessionID
	ParentID *session.SessionID
}

// Registry owns Agent membership and parent-child constraints. Its actor owner
// serializes operations and performs creation and shutdown outside this object.
type Registry struct {
	// admissionStopped is permanent for this registry instance. Membership stays
	// available until ordinary descendant-first removal completes.
	admissionStopped bool
	// entries maps Agent IDs to their registered identity and optional parent ID.
	// Membership does not imply that component initialization has completed.
	entries map[session.SessionID]membership
}

// Register adds a fresh identity under an already registered parent, if any.
func (directory *Registry) Register(entry Registration) error {
	if directory.admissionStopped {
		return ErrClosing
	}
	if !entry.Role.Valid() || entry.Role == CompanionRole && entry.ParentID == nil {
		return ErrRole
	}
	if entry.ID == "" {
		return errors.New("agent: registration ID is required")
	}
	previous, found := directory.entries[entry.ID]
	if found && previous.lifecycle != Closed {
		return errors.New("agent: identity is already registered")
	}
	if entry.ParentID != nil {
		if *entry.ParentID == entry.ID {
			return errors.New("agent: parent cannot be self")
		}
		err := directory.CheckAdmission(*entry.ParentID)
		if err != nil {
			return err
		}
	}
	if entry.Role == CompanionRole {
		parent := directory.entries[*entry.ParentID]
		if parent.registration.Role == CompanionRole {
			return ErrRole
		}
	}
	if directory.entries == nil {
		directory.entries = make(map[session.SessionID]membership)
	}
	directory.entries[entry.ID] = membership{
		registration: cloneRegistration(entry),
		lifecycle:    Initializing,
	}
	return nil
}

// Get returns a detached registration when the identity is present.
func (directory *Registry) Get(identifier session.SessionID) (Registration, bool) {
	entry, found := directory.entries[identifier]
	return cloneRegistration(entry.registration), found && entry.lifecycle != Closed
}

// List returns detached registrations ordered by Agent ID.
func (directory *Registry) List() []Registration {
	result := make([]Registration, 0, len(directory.entries))
	for _, entry := range directory.entries {
		if entry.lifecycle != Closed {
			result = append(result, cloneRegistration(entry.registration))
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

// Children returns the directly registered children in stable ID order.
func (directory *Registry) Children(identifier session.SessionID) []session.SessionID {
	result := make([]session.SessionID, 0)
	for _, entry := range directory.entries {
		if entry.lifecycle != Closed && entry.registration.ParentID != nil && *entry.registration.ParentID == identifier {
			result = append(result, entry.registration.ID)
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
	entry, found := directory.entries[identifier]
	if !found || entry.lifecycle == Closed {
		return nil
	}
	if entry.lifecycle != Closing {
		return errors.New("agent: close must begin before identity removal")
	}
	if len(entry.resources) != 0 {
		return errors.New("agent: resource release is not confirmed")
	}
	entry.lifecycle = Closed
	directory.entries[identifier] = entry
	return nil
}

// BeginClose stops new admission beneath an existing identity without removing
// any child or running resource cleanup. Repeated calls are idempotent.
func (directory *Registry) BeginClose(identifier session.SessionID) error {
	entry, found := directory.entries[identifier]
	if !found || entry.lifecycle == Closed {
		return errors.New("agent: identity is not registered")
	}
	entry.lifecycle = Closing
	directory.entries[identifier] = entry
	return nil
}

// CheckAdmission rejects missing identities and closing ancestors. It does not
// establish readiness or authorize a particular framework creation.
func (directory *Registry) CheckAdmission(identifier session.SessionID) error {
	if directory.admissionStopped {
		return ErrClosing
	}
	for {
		entry, found := directory.entries[identifier]
		if !found || entry.lifecycle == Closed {
			return errors.New("agent: identity is not registered")
		}
		if entry.lifecycle == Closing {
			return ErrClosing
		}
		if entry.registration.ParentID == nil {
			return nil
		}
		identifier = *entry.registration.ParentID
	}
}

// StopAdmission prevents new membership and ordinary access without removing
// entries or claiming their resources have closed. Repeated calls are harmless.
func (directory *Registry) StopAdmission() {
	directory.admissionStopped = true
}

func cloneRegistration(entry Registration) Registration {
	if entry.ParentID != nil {
		identifier := *entry.ParentID
		entry.ParentID = &identifier
	}
	return entry
}
