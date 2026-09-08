// Package projectionactor exposes projection operations through GoAkt messages.
package projectionactor

import (
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/projection"
)

// ChangedTopic carries complete projection values after successful folds.
const ChangedTopic = "session/projection"

// Register installs a domain-owned pure computation.
type Register struct {
	Unit projection.Unit
}

// Registered returns the independently removable registration identity.
type Registered struct {
	ID  projection.RegistrationID
	Err error
}

// Remove releases one registration without affecting later registrations.
type Remove struct {
	ID projection.RegistrationID
}

// Completion acknowledges a mutation with no payload.
type Completion struct {
	Err error
}

// Read computes all views at the supplied committed log boundary.
type Read struct {
	SessionID  session.SessionID
	CreationID string
	Events     []session.Event
}

// Viewed contains detached projection values.
type Viewed struct {
	Snapshot projection.Snapshot
	Err      error
}

// Capture computes versioned checkpoint rows at the supplied log boundary.
type Capture struct {
	SessionID  session.SessionID
	CreationID string
	Events     []session.Event
}

// Captured contains detached rebuildable states, not a durability acknowledgement.
type Captured struct {
	Checkpoint projection.Checkpoint
	Err        error
}

// Forget releases folds for exactly one Session creation.
type Forget struct {
	SessionID  session.SessionID
	CreationID string
}

// RestoreFloor asks for the storage tail anchor needed by registered Units.
type RestoreFloor struct {
	Checkpoint projection.Checkpoint
}

// Floor contains the one-below anchor; nil means no Units are registered.
type Floor struct {
	Seq *int64
}

// ViewCheckpoint converts compatible cached states without reading storage.
type ViewCheckpoint struct {
	Checkpoint projection.Checkpoint
}

// Values contains detached views of compatible checkpoint rows.
type Values struct {
	Values projection.Values
	Err    error
}

// Restore rebuilds a checkpoint from a contiguous persisted log suffix.
type Restore struct {
	Checkpoint projection.Checkpoint
	Events     []session.Event
	BaseSeq    int64
}

// Restored contains the rebuilt snapshot and refreshed checkpoint.
type Restored struct {
	Result projection.RestoreResult
	Err    error
}

// Subscribed confirms that Session append events can reach the projection owner.
type Subscribed struct{}

// ChangeNotice publishes a derived view change without persisting the notice.
// Change is the calculation result; durable recovery still uses Session events.
type ChangeNotice struct {
	SessionID  session.SessionID
	CreationID string
	Change     projection.Change
}

// Compute projects an explicit history without modifying a retained live fold.
type Compute struct {
	Events []session.Event
}
