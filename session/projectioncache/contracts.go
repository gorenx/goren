// Package projectioncache owns rebuildable Session projection checkpoints.
// Durable Session events remain the only source of truth.
package projectioncache

import (
	"context"
	"time"

	"github.com/gorenx/goren/session"
	sessionpersistence "github.com/gorenx/goren/session/persistence"
)

const (
	// DefaultWriteEveryEvents checkpoints after this many observed live events.
	DefaultWriteEveryEvents = 200
	// DefaultWriteInterval bounds how long dirty live state waits for a checkpoint.
	DefaultWriteInterval = 5 * time.Second
)

// CheckpointStore persists one replaceable checkpoint record per Session.
// Replace borrows its record for the duration of the call and must not mutate it.
type CheckpointStore interface {
	LoadAll(context.Context) (map[session.SessionID]CheckpointRecord, error)
	Replace(context.Context, session.SessionID, CheckpointRecord) error
	Close(context.Context) error
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

// Config controls checkpoint scheduling, not physical storage.
type Config struct {
	WriteEveryEvents int
	WriteInterval    time.Duration
}
