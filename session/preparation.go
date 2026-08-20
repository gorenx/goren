package session

import "sync"

// PreparationLease owns provider state reserved for one unpublished Session.
type PreparationLease interface {
	Release()
}

// Preparation owns one exact unpublished Session returned by a persistence
// provider. Release is idempotent; publication may consume the provider state
// first, in which case the callback becomes a no-op.
type Preparation struct {
	conversation *Session
	lease        PreparationLease
	once         sync.Once
}

// NewPreparation associates an unpublished Session with its provider lease.
func NewPreparation(conversation *Session, lease PreparationLease) *Preparation {
	return &Preparation{
		conversation: conversation,
		lease:        lease,
	}
}

// UnpublishedSession returns the exact Session that must be published.
func (owned *Preparation) UnpublishedSession() *Session {
	if owned == nil {
		return nil
	}
	return owned.conversation
}

// Dispose relinquishes unpublished provider state once.
func (owned *Preparation) Dispose() {
	if owned == nil {
		return
	}
	owned.once.Do(func() {
		if owned.lease != nil {
			owned.lease.Release()
		}
	})
}
