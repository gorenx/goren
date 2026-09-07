// Package projection owns the registry that drives domain-defined
// read projections over committed Session events.
package projection

import (
	"encoding/json"

	"github.com/gorenx/goren/session"
)

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

// Snapshot is one consistent read of all currently registered units.
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

// RegistrationID identifies one independently removable Unit registration.
type RegistrationID string

// Change is one whole projection value caused by one committed event.
type Change struct {
	Key   string
	Value json.RawMessage
	Seq   int64
}
