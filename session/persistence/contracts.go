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
	Locate(session.Header) (Location, bool)
	SupportsRawArtifacts() bool
	ReadRaw(context.Context, session.SessionID) (RawArtifact, bool, error)
	Create(context.Context, session.Header) error
	Append(context.Context, session.SessionID, []session.Event) error
	Prepare(context.Context, session.SessionID) (*session.Preparation, error)
	Load(context.Context, session.SessionID) (Inspection, error)
	Inspect(context.Context, session.SessionID) (Inspection, error)
	ReadFrom(context.Context, session.SessionID, int64) (Inspection, error)
	List(context.Context) ([]session.Header, error)
	ListSnapshots(context.Context) ([]Snapshot, error)
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

// Backend is the storage-only port. Implementations map rows/files and execute
// requested transactions; recovery and live Session policy remain above it.
type Backend interface {
	BackendName() string
	Locate(session.Header) (Location, bool)
	SupportsRawArtifacts() bool
	ReadRaw(context.Context, session.SessionID) (RawArtifact, bool, error)
	LoadStored(context.Context, session.SessionID) (StoredPrefix, bool, error)
	ReadStoredRevision(context.Context, session.SessionID) (Revision, bool, error)
	LoadStoredFrom(context.Context, session.SessionID, int64) (StoredSuffix, bool, error)
	AppendBatch(context.Context, session.Header, []session.Event, bool) error
	CommitRepair(context.Context, session.Header, RepairMarker, []session.Event) error
	ListStored(context.Context) ([]session.Header, error)
	ListStoredSnapshots(context.Context) ([]Snapshot, error)
	Close(context.Context) error
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
