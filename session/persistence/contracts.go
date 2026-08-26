// Package persistence owns durable Session-log orchestration and the
// consumer-owned storage port implemented by concrete persistence adapters.
package persistence

import (
	"context"
	"errors"

	"github.com/gorenx/goren/plugin"
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

// Persistence is the backend-neutral durable Session capability. Callers do
// not observe whether its SessionLogStore uses a JSONL, SQLite, or other
// Backend adapter.
type Persistence interface {
	plugin.Service
	Locate(metadata session.Header) (Location, bool)
	SupportsRawArtifacts() bool
	ReadRaw(ctx context.Context, id session.SessionID) (RawArtifact, error)
	Create(ctx context.Context, metadata session.Header) error
	Append(ctx context.Context, id session.SessionID, events []session.Event) error
	Prepare(ctx context.Context, id session.SessionID) (*session.Preparation, error)
	Load(ctx context.Context, id session.SessionID) (Inspection, error)
	Inspect(ctx context.Context, id session.SessionID) (Inspection, error)
	ReadFrom(ctx context.Context, id session.SessionID, fromSeq int64) (Inspection, error)
	ReadEventsBefore(ctx context.Context, id session.SessionID, beforeSeq *int64, limit int64) (EventWindow, error)
	List(ctx context.Context) ([]session.Header, error)
	ListSnapshots(ctx context.Context) ([]Snapshot, error)
}

const PluginName = "@deepseek-ai/dsh-session-persistence"

// BackendOpener acquires one configured storage Backend during Plugin Apply.
type BackendOpener interface {
	OpenBackend(context.Context) (Backend, error)
}

// BackgroundWriteFailure identifies one failed asynchronous durability batch.
type BackgroundWriteFailure struct {
	BackendName string
	SessionID   session.SessionID
	Error       error
}

// BackgroundWriteFailureReporter receives failures outside a caller-owned
// flush boundary.
type BackgroundWriteFailureReporter interface {
	ReportBackgroundWriteFailure(BackgroundWriteFailure)
}

// RepairMarker is opaque backend state required to remove a torn physical tail.
type RepairMarker interface {
	PersistenceRepairMarker()
}

// StoredPrefix is a fresh physical snapshot read by SessionLogStore.
type StoredPrefix struct {
	Header session.Header
	Events []session.Event
	Token  Revision
	Marker RepairMarker
}

// StoredSuffix is a seek-capable physical suffix returned without repair.
type StoredSuffix struct {
	Header session.Header
	Events []session.Event
}

// StoredEventWindow is a physical bounded read returned newest-first.
type StoredEventWindow struct {
	Header     session.Header
	Events     []session.Event
	HasEarlier bool
}

// EventPage identifies one backward page of stored Session Events.
type EventPage struct {
	BeforeSeq *int64
	Limit     int64
}

// EventBatch is one atomic Session Header/Event append transaction.
// Materialize requires the Backend to create Header and Events together.
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

// Backend is the storage-only port. Implementations map rows/files and execute
// requested transactions; recovery and live Session policy remain above it.
// Optional reads return nil when no stored record exists and a non-nil error
// only when the storage operation itself fails.
type Backend interface {
	BackendName() string
	Locate(metadata session.Header) (Location, bool)
	SupportsRawArtifacts() bool
	ReadRaw(ctx context.Context, id session.SessionID) (*RawArtifact, error)
	LoadStored(ctx context.Context, id session.SessionID) (*StoredPrefix, error)
	ReadStoredRevision(ctx context.Context, id session.SessionID) (*Revision, error)
	LoadStoredFrom(ctx context.Context, id session.SessionID, fromSeq int64) (*StoredSuffix, error)
	LoadStoredEventsBefore(ctx context.Context, id session.SessionID, page EventPage) (*StoredEventWindow, error)
	AppendBatch(ctx context.Context, batch EventBatch) error
	CommitRepair(ctx context.Context, repair LogRepair) error
	ListStored(ctx context.Context) ([]session.Header, error)
	ListStoredSnapshots(ctx context.Context) ([]Snapshot, error)
	Close(closeContext context.Context) error
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
