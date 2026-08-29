// Package projectioncache owns rebuildable Session projection checkpoints.
// Durable Session events remain the only source of truth.
package projectioncache

import (
	"context"
	"time"

	"github.com/gorenx/goren/session"
	sessionpersistence "github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

const (
	// PluginName is the canonical Session Projection Cache plugin name.
	PluginName = "@deepseek-ai/dsh-session-projection-cache"
	// DefaultWriteEveryEvents checkpoints after this many observed live events.
	DefaultWriteEveryEvents = 200
	// DefaultWriteInterval bounds how long dirty live state waits for a checkpoint.
	DefaultWriteInterval = 5 * time.Second
)

// Cache offers read-only checkpoint reuse to Session read use cases.
type Cache interface {
	CachedSnapshot(session.Header) (*sessionprojection.Snapshot, error)
	ColdSnapshot(context.Context, session.SessionID) (sessionprojection.Snapshot, error)
}

// CheckpointStore persists one replaceable checkpoint record per Session.
// Replace borrows its record for the duration of the call and must not mutate it.
type CheckpointStore interface {
	LoadAll(context.Context) (map[session.SessionID]CheckpointRecord, error)
	Replace(context.Context, session.SessionID, CheckpointRecord) error
	Close(context.Context) error
}

// LiveSessionFlusher is the minimal live-Session durability boundary consumed
// before publishing a non-final checkpoint.
type LiveSessionFlusher interface {
	Flush(context.Context, session.Context) error
}

// DurableEventReader supplies the durable Session facts used for cold rebuild.
type DurableEventReader interface {
	ReadEventsFrom(
		context.Context,
		session.SessionID,
		sessionpersistence.EventContinuation,
	) (sessionpersistence.EventSegment, error)
	ReadEventsBefore(
		context.Context,
		session.SessionID,
		sessionpersistence.EventPage,
	) (sessionpersistence.EventWindow, error)
}

// CheckpointProjector performs pure projection checkpoint/view/restore work.
type CheckpointProjector interface {
	// Checkpoint captures every registered projection at one live Session cut.
	Checkpoint(session.Context) (sessionprojection.Checkpoint, error)
	// RestoreFloor selects the first durable event sequence not covered by all
	// compatible rows; nil means no registered projection needs event replay.
	RestoreFloor(sessionprojection.Checkpoint) *int64
	// ViewCheckpoint converts version-compatible rows into model-facing values
	// without reading or mutating the Session log.
	ViewCheckpoint(sessionprojection.Checkpoint) (sessionprojection.Values, error)
	// Restore folds one durable event page onto prior rows and returns both the
	// updated complete checkpoint and its current value snapshot.
	Restore(
		sessionprojection.Checkpoint,
		[]session.Event,
		int64,
	) (sessionprojection.RestoreResult, error)
}

// Failure identifies contained cache work that cannot change Session truth.
type Failure struct {
	SessionID session.SessionID
	Operation string
	Error     error
}

// FailureReporter receives contained checkpoint write and write-back failures.
type FailureReporter interface {
	ReportProjectionCacheFailure(Failure)
}

// Config controls checkpoint scheduling, not physical storage.
type Config struct {
	WriteEveryEvents int
	WriteInterval    time.Duration
	Failures         FailureReporter
}
