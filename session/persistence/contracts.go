// Package persistence owns durable read validation, recovery rules and storage ports.
package persistence

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
)

// Revision is an opaque source-qualified token for one stored log version.
type Revision string

// Snapshot is the lightweight metadata and revision view used by read models.
type Snapshot struct {
	Header   session.Header
	Revision Revision
}

// Inspection is one validated logical Session view detached from storage.
type Inspection struct {
	Header session.Header
	Events []session.Event
}

// EventWindow is one contiguous newest-first event range ending before a
// caller-selected sequence. HasEarlier reports whether an older range exists.
type EventWindow struct {
	Header     session.Header
	Events     []session.Event
	HasEarlier bool
}

// EventSegment is one contiguous oldest-first event page. Revision identifies
// the durable log version observed by that page so multi-page consumers can
// reject a projection assembled across different versions.
type EventSegment struct {
	Header   session.Header
	Revision Revision
	Events   []session.Event
	HasMore  bool
}

// SessionCursor identifies the last Session in a newest-first listing page.
// CreatedAt and ID together provide a stable order when timestamps are equal.
type SessionCursor struct {
	CreatedAt int64
	ID        session.SessionID
}

// SessionPage requests one bounded page after Cursor. A nil Cursor starts at
// the newest Session.
type SessionPage struct {
	Cursor *SessionCursor
	Limit  int64
}

// HeaderPage is one page of durable Session headers.
type HeaderPage struct {
	Headers    []session.Header
	NextCursor *SessionCursor
}

// SnapshotPage is one page of durable Session headers and revisions.
type SnapshotPage struct {
	Snapshots  []Snapshot
	NextCursor *SessionCursor
}

// Location identifies a backend-owned per-Session artifact when one exists.
type Location struct {
	Kind string
	Path string
}

// RawArtifact is the exact text of a backend-owned per-Session artifact.
type RawArtifact struct {
	Header   session.Header
	Filename string
	Content  string
}

// ReadStorage is the storage-only port consumed by Reader. It cannot mutate logs
// or close the shared adapter; Session actors and composition own those operations.
// Optional reads return nil when no record exists; storage failures return errors.
type ReadStorage interface {
	Locate(metadata session.Header) (Location, bool)
	SupportsRawArtifacts() bool
	ReadRaw(ctx context.Context, id session.SessionID) (*RawArtifact, error)
	Load(ctx context.Context, id session.SessionID) (*Log, error)
	ReadEventsFrom(ctx context.Context, id session.SessionID, continuation EventContinuation) (*EventSegment, error)
	ReadEventsBefore(ctx context.Context, id session.SessionID, page EventPage) (*EventWindow, error)
	List(ctx context.Context, page SessionPage) (SnapshotPage, error)
}

// RepairMarker is opaque backend state required to remove a torn physical tail.
type RepairMarker interface {
	PersistenceRepairMarker()
}

// Log is one fresh physical Session log read for inspection or recovery. Marker is
// present only when storage found a torn tail that recovery may remove.
type Log struct {
	Header   session.Header
	Events   []session.Event
	Revision Revision
	Marker   RepairMarker
}

// EventContinuation requests one oldest-first page beginning at FromSeq.
type EventContinuation struct {
	FromSeq int64
	Limit   int64
}

// EventPage requests one newest-first page before BeforeSeq. A nil BeforeSeq
// starts at the durable log tail.
type EventPage struct {
	BeforeSeq *int64
	Limit     int64
}

// EventBatch is one atomic Session Header/Event append transaction.
// Materialize requires storage to create Header and Events together.
type EventBatch struct {
	Header      session.Header
	Events      []session.Event
	Materialize bool
}

// LogRepair is one atomic torn-tail removal and closing-event transaction.
type LogRepair struct {
	Header        session.Header
	Marker        RepairMarker
	ClosingEvents []session.Event
}

// NotFoundError reports an absent materialized Session identity.
type NotFoundError struct {
	ID session.SessionID
}

func (problem *NotFoundError) Error() string {
	return "session persistence: session \"" + string(problem.ID) + "\" not found"
}

// CorruptionError distinguishes invalid stored bytes/rows from an unsupported
// but intact format.
type CorruptionError struct {
	ID    session.SessionID
	Cause error
}

func (problem *CorruptionError) Error() string {
	return "session persistence: stored session \"" + string(problem.ID) + "\" failed validation: " + problem.Cause.Error()
}

func (problem *CorruptionError) Unwrap() error { return problem.Cause }

// UnsupportedFormatError refuses an intact log this build cannot interpret.
type UnsupportedFormatError struct {
	ID       session.SessionID
	Reason   string
	Location *Location
}

func (problem *UnsupportedFormatError) Error() string {
	if problem.Location == nil {
		return problem.Reason
	}
	return problem.Reason + " (raw log: " + problem.Location.Path + ")"
}

var errRawArtifactsUnavailable = errors.New("session persistence: backend does not expose raw artifacts")
