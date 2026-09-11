package registry

import (
	"errors"
	"github.com/gorenx/goren/session"
)

// authority owns creation reservations inside the Session lifecycle implementation.
// Its caller serializes access; no log or mutable Actor reference is retained.
type authority struct {
	// registrations maps each stable SessionID to its current creation grant.
	// The value records the creation ID, the one authorized Grain activation, and
	// whether admission has been revoked. Absence means the identity is available.
	// Release removes the entry only after shutdown; this is not a Grain address map.
	registrations map[session.SessionID]registration
}

type registration struct {
	// creationID identifies one business creation supplied by its coordinator.
	// Retries reuse it; reopening after close requires a new value, unlike SessionID.
	// It rejects close and release requests left over from an earlier creation.
	creationID string
	// instanceID identifies the Grain activation authorized by the first Claim.
	// Reactivation produces a new value and cannot reuse the old creation grant.
	// Empty means unclaimed. This value does not control framework routing or scheduling.
	instanceID string
	retired    bool
}

// Reserve holds an identity until its original creation is retired and released.
func (directory *authority) Reserve(identifier session.SessionID, creationID string) error {
	if identifier == "" || creationID == "" {
		return errors.New("session: Session and creation IDs are required")
	}
	if previous, exists := directory.registrations[identifier]; exists {
		if previous.creationID == creationID && !previous.retired {
			return nil
		}
		return errors.New("session: identity is reserved or retired")
	}
	if directory.registrations == nil {
		directory.registrations = make(map[session.SessionID]registration)
	}
	directory.registrations[identifier] = registration{
		creationID: creationID,
	}
	return nil
}

// Claim binds a creation to one physical instance; a reactivated instance must not reuse it.
func (directory *authority) Claim(identifier session.SessionID, creationID string, instanceID string) error {
	previous, exists := directory.registrations[identifier]
	if !exists || previous.retired || previous.creationID != creationID || instanceID == "" {
		return errors.New("session: creation is not authorized")
	}
	if previous.instanceID != "" && previous.instanceID != instanceID {
		return errors.New("session: creation already belongs to another instance")
	}
	previous.instanceID = instanceID
	directory.registrations[identifier] = previous
	return nil
}

// Retire revokes initialization authority without releasing the business identity.
func (directory *authority) Retire(identifier session.SessionID, creationID string) error {
	previous, exists := directory.registrations[identifier]
	if !exists || creationID == "" || previous.creationID != creationID {
		return errors.New("session: unknown creation reservation")
	}
	previous.retired = true
	directory.registrations[identifier] = previous
	return nil
}

// Release forgets a retired creation after its coordinator confirms complete shutdown.
// A stale creation cannot release another reservation with the same SessionID.
func (directory *authority) Release(identifier session.SessionID, creationID string) error {
	previous, exists := directory.registrations[identifier]
	if !exists {
		return nil
	}
	if previous.creationID != creationID || !previous.retired {
		return errors.New("session: reservation cannot be released")
	}
	delete(directory.registrations, identifier)
	return nil
}

// Check verifies that a lifecycle operation still owns the reservation.
// A retired creation remains its owner until the coordinator releases it.
func (directory *authority) Check(identifier session.SessionID, creationID string) error {
	previous, exists := directory.registrations[identifier]
	if !exists || creationID == "" || previous.creationID != creationID {
		return errors.New("session: unknown creation reservation")
	}
	return nil
}
