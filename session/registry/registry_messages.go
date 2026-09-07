package registry

import (
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
)

// Reserve holds a Session identity for the original creation transaction.
type Reserve struct {
	SessionID session.SessionID
	// CreationID is supplied by the coordinator; retries reuse it, new creations must not.
	CreationID string
}

// Retire revokes initialization authority while keeping the identity occupied.
type Retire struct {
	SessionID session.SessionID
	// CreationID selects the grant to revoke without affecting a replacement creation.
	CreationID string
}

// Release removes a retired reservation after actual shutdown is confirmed.
type Release struct {
	SessionID session.SessionID
	// CreationID selects the original reservation so late releases cannot remove its replacement.
	CreationID string
}

// Completion preserves the business result of a reservation mutation.
type Completion struct {
	Err error
}

// CloseSession closes the log, revokes creation authority, deactivates the Grain,
// and releases its reservation. The caller must first finish Agent work.
type CloseSession struct {
	SessionID session.SessionID
	// CreationID selects the creation to close and correlates retries with the original result.
	CreationID string
}

// CreateSession reserves and initializes a fresh Session through the Registry.
type CreateSession struct {
	SessionID session.SessionID
	// CreationID identifies this creation; concurrent calls and retries retain its original input.
	CreationID string
	Options    session.CreateOptions
	Storage    sessionactor.Storage
}

// ResumeSession reserves and restores an existing durable Session.
type ResumeSession struct {
	SessionID session.SessionID
	// CreationID identifies this recovery; retries cannot switch to creating a fresh log.
	CreationID string
	Storage    sessionactor.RecoveryStorage
}

// RegisterSession fixes initialization input without activating a Session Grain.
type RegisterSession struct {
	SessionID session.SessionID
	// CreationID identifies the reservation; repeated registration cannot change its input.
	CreationID string
	// Restore selects durable recovery. Options must be empty when Restore is true.
	Restore bool
	Options session.CreateOptions
	Storage sessionactor.RecoveryStorage
}

// StartSession initializes or re-reads the Session using previously registered input.
type StartSession struct {
	SessionID session.SessionID
	// CreationID must match the registration; stale creation events cannot replace it.
	CreationID string
}

// ListSessions requests initialized Sessions whose creation admission remains valid.
// It does not activate grains, perform I/O, or freeze subsequent lifecycle changes.
type ListSessions struct{}

// Session is immutable creation metadata for a currently admitted Session.
type Session struct {
	Header session.Header
	// CreationID authorizes downstream reads and stays in the actor contract.
	CreationID string
}

// Sessions is a detached list sorted by stable Session identity.
type Sessions struct {
	Items []Session
}
