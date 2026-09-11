package projectioncache

import (
	"errors"
	"time"

	"github.com/gorenx/goren/session"
)

// Progress owns checkpoint trigger rules for one log, not execution or timer state.
type Progress struct {
	every    int64
	interval time.Duration
	// observedNext is the first sequence not yet observed or covered by a checkpoint.
	// For example, observing events 0 through 9 sets observedNext to 10.
	observedNext int64
	// checkpointNext is the first sequence outside the last successfully saved prefix.
	// For example, a checkpoint saved through event 5 sets checkpointNext to 6.
	// With observedNext == 10, events 6 through 9 still need checkpointing.
	// A failed checkpoint write leaves this value unchanged; it is not the Session
	// log flush position, which may already be ahead of the checkpoint.
	checkpointNext int64
	// turnEndNext marks the latest observed turn boundary; covered boundaries are inert.
	turnEndNext int64
	// deadline bounds the age of dirty input; zero means no unsaved input is known.
	deadline time.Time
}

// NewProgress resumes trigger accounting from a confirmed checkpoint prefix.
func NewProgress(settings Config, nextSeq int64) (*Progress, error) {
	every := settings.WriteEveryEvents
	if every == 0 {
		every = DefaultWriteEveryEvents
	}
	interval := settings.WriteInterval
	if interval == 0 {
		interval = DefaultWriteInterval
	}
	if every < 1 || interval < time.Millisecond || nextSeq < 0 || nextSeq > 1<<53-1 {
		return nil, errors.New("session projection cache: invalid trigger configuration or checkpoint prefix")
	}
	return &Progress{
		every:          int64(every),
		interval:       interval,
		observedNext:   nextSeq,
		checkpointNext: nextSeq,
	}, nil
}

// Observe records new log progress without counting duplicate or late notifications.
func (owner *Progress) Observe(entry session.Event, observedAt time.Time) error {
	if entry.Seq < 0 || entry.Seq >= 1<<53-1 {
		return errors.New("session projection cache: invalid event sequence")
	}
	if entry.Seq < owner.observedNext {
		return nil
	}
	owner.observedNext = entry.Seq + 1
	if owner.deadline.IsZero() {
		owner.deadline = observedAt.Add(owner.interval)
	}
	if entry.Type == session.TurnEndEventName {
		owner.turnEndNext = entry.Seq + 1
	}
	return nil
}

// Due reports whether the unsaved prefix meets an event, turn-end, or time trigger.
func (owner *Progress) Due(now time.Time) bool {
	if owner.observedNext <= owner.checkpointNext {
		return false
	}
	return owner.observedNext-owner.checkpointNext >= owner.every ||
		owner.turnEndNext > owner.checkpointNext ||
		(!owner.deadline.IsZero() && !now.Before(owner.deadline))
}

// Deadline returns the business deadline for the framework to schedule, if dirty.
func (owner *Progress) Deadline() (time.Time, bool) {
	return owner.deadline, !owner.deadline.IsZero()
}

// Confirm advances only the prefix acknowledged by successful checkpoint storage.
// Events arriving during a write remain dirty when they are beyond that prefix.
func (owner *Progress) Confirm(nextSeq int64) error {
	if nextSeq < owner.checkpointNext || nextSeq > 1<<53-1 {
		return errors.New("session projection cache: invalid checkpoint progress confirmation")
	}
	owner.checkpointNext = nextSeq
	owner.observedNext = max(owner.observedNext, nextSeq)
	if owner.observedNext == owner.checkpointNext {
		owner.deadline = time.Time{}
	}
	return nil
}
