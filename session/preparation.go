package session

import "sync"

// Preparation owns one exact unpublished Session returned by a persistence
// provider. Release is idempotent; publication may consume the provider state
// first, in which case the callback becomes a no-op.
type Preparation struct {
	conversation *Session
	cleanup      func()
	once         sync.Once
}

// NewPreparation wraps an unpublished Session and an optional provider-state
// release callback.
func NewPreparation(conversation *Session, cleanup func()) *Preparation {
	return &Preparation{conversation: conversation, cleanup: cleanup}
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
		if owned.cleanup != nil {
			owned.cleanup()
		}
	})
}
