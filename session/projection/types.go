// Package sessionprojection owns the registry that drives domain-defined
// read projections over committed Session events.
package projection

import (
	"encoding/json"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

const ServiceName = "sessionProjections"

// Service is the canonical session-projection capability identity.
var Service = plugin.DefineService[Registry](ServiceName)

// Transition is one unit's next plain-JSON state. Changed is explicit because
// Go interfaces do not have TypeScript's Object.is reference-identity gate.
type Transition struct {
	State   json.RawMessage
	Changed bool
}

// Unit is one domain-owned synchronous projection computation. State and
// values cross the registry boundary as detached plain JSON so the registry
// can cache them without knowing domain-specific Go types.
type Unit interface {
	Key() string
	StateVersion() int64
	InitialState() (json.RawMessage, error)
	ApplyState(json.RawMessage, session.Event) (Transition, error)
	ViewState(json.RawMessage) (json.RawMessage, error)
}

// Values contains one whole JSON value for every registered projection key.
type Values map[string]json.RawMessage

// Snapshot is one consistent read cut over all currently registered units.
type Snapshot struct {
	AsOfSeq int64  `json:"asOfSeq"`
	Values  Values `json:"values"`
}

// CheckpointRow is one non-authoritative cached unit state.
type CheckpointRow struct {
	Version int64           `json:"ver"`
	Seq     int64           `json:"seq"`
	Value   json.RawMessage `json:"val"`
}

// Checkpoint contains cached state rows keyed by projection key.
type Checkpoint map[string]CheckpointRow

// RestoreResult returns both the restored wire snapshot and refreshed cache.
type RestoreResult struct {
	Snapshot   Snapshot
	Checkpoint Checkpoint
}

// Change is one whole projection value caused by one committed event.
type Change struct {
	Session *session.Session
	Key     string
	Value   json.RawMessage
	Seq     int64
}

// ChangeListener consumes projection changes without exposing an untyped
// callback payload.
type ChangeListener interface {
	ProjectionChanged(Change)
}

// ChangeListenerFunc adapts one typed function to ChangeListener.
type ChangeListenerFunc func(Change)

// ProjectionChanged invokes the adapted function.
func (operation ChangeListenerFunc) ProjectionChanged(projectionChange Change) {
	operation(projectionChange)
}

// Registry owns registrations, per-session folds, snapshots, and cache
// restore. Domain units own only their pure projection mathematics.
type Registry interface {
	Register(*plugin.Scope, Unit) (plugin.Disposer, error)
	OnChanged(*plugin.Scope, ChangeListener) (plugin.Disposer, error)
	Snapshot(*session.Session) (Snapshot, error)
	Checkpoint(*session.Session) (Checkpoint, error)
	RestoreFloor(Checkpoint) *int64
	ViewCheckpoint(Checkpoint) (Values, error)
	Restore(Checkpoint, []session.Event, int64) (RestoreResult, error)
}
