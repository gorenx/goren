// Package sessionactor owns the GoAkt boundary for Session business state.
package sessionactor

import (
	"context"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
)

// Initialize supplies one Registry-authorized creation input to a Session identity.
type Initialize struct {
	CreationID string
	Options    session.CreateOptions
	Storage    Storage
}

// Initialized reports the Session header and currently committed prefix.
type Initialized struct {
	Header  session.Header
	Barrier session.WriteBarrier
	Err     error
}

// Commit submits a deterministic business batch under the original creation identity.
type Commit struct {
	CreationID string
	Plan       session.WritePlan
}

// Committed acknowledges memory commitment, not storage durability.
type Committed struct {
	Result session.WriteResult
	Err    error
}

// Read captures a consistent view at or beyond an optional required prefix.
type Read struct {
	CreationID string
	Minimum    *session.WriteBarrier
}

// View returns detached committed state, never a mutable Session owner.
type View struct {
	Snapshot session.Snapshot
	Err      error
}

// Storage is the Session-owned port for atomic persistence operations.
// The composition root retains ownership of shared adapter shutdown.
type Storage interface {
	Load(ctx context.Context, identifier session.SessionID) (stored *persistence.Log, err error)
	AppendBatch(ctx context.Context, batch persistence.EventBatch) error
}

// Flush requests durability through one committed prefix.
type Flush struct {
	CreationID string
	Barrier    session.WriteBarrier
}

// Flushed reports the actual durable prefix under the selected Backend guarantees.
type Flushed struct {
	Barrier session.WriteBarrier
	Err     error
}

// Close seals writes and waits for all committed facts to become durable.
type Close struct {
	CreationID string
}

// Closed acknowledges completed Session business drain, not shared backend shutdown.
type Closed struct {
	Barrier session.WriteBarrier
	Err     error
}

// RecoveryStorage adds revision checks and owner-decided repairs to normal storage.
type RecoveryStorage interface {
	Storage
	Revision(ctx context.Context, identifier session.SessionID) (revision *persistence.Revision, err error)
	CommitRepair(ctx context.Context, repair persistence.LogRepair) error
}

// Resume restores a stored Session under a fresh creation identity.
type Resume struct {
	CreationID string
	Storage    RecoveryStorage
}
