package companion

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/gorenx/goren/session"
)

// Record freezes a conditional relationship update for the ordinary Session log.
// It contains no resource operation and grants no permission to start a child.
type Record struct {
	next     Companion
	expected int64
}

var _ session.IdempotentWrite = (*Record)(nil)

// NewRecord validates and detaches an update before it enters the Session queue.
func NewRecord(next Companion, expected int64) (*Record, error) {
	if err := next.Validate(); err != nil {
		return nil, err
	}
	if expected < 0 || expected >= maximumRevision || next.Revision != expected+1 {
		return nil, ErrConflict
	}
	return &Record{
		next:     next.Clone(),
		expected: expected,
	}, nil
}

// Build rechecks current state under Session serialization. Durability remains
// the caller's responsibility: Flush the returned barrier before acquiring resources.
func (plan *Record) Build(_ context.Context, captured session.Snapshot) ([]session.EventDraft, error) {
	if captured.Barrier.SessionID != plan.next.ParentSessionID {
		return nil, ErrInvalid
	}
	current, err := CompanionsFromEvents(plan.next.ParentSessionID, captured.Events)
	if err != nil {
		return nil, err
	}
	var previous Companion
	var exists bool
	for _, entry := range current {
		if entry.Name == plan.next.Name {
			previous = entry
			exists = true
		}
	}
	if previous.Revision != plan.expected {
		return nil, ErrConflict
	}
	if err := validateTransition(previous, exists, plan.next); err != nil {
		return nil, err
	}
	if !exists || previous.ChildSessionID != plan.next.ChildSessionID {
		// Retain child uniqueness across tombstones and prior names, including
		// generations no longer present in the latest management view.
		for _, entry := range captured.Events {
			if entry.Type != "companion/instance" {
				continue
			}
			var recorded Companion
			if json.Unmarshal(entry.Data, &recorded) != nil {
				return nil, ErrHistory
			}
			if recorded.ParentSessionID == plan.next.ParentSessionID && recorded.ChildSessionID == plan.next.ChildSessionID {
				return nil, ErrConflict
			}
		}
	}
	draft, err := session.NewEventDraft(CompanionRecorded, plan.next)
	if err != nil {
		return nil, err
	}
	return []session.EventDraft{draft}, nil
}

// FindCommit returns the original barrier for an identical retry, even if later
// changes have advanced the Companion. A reused revision with different data fails.
func (plan *Record) FindCommit(captured session.Snapshot) (*session.WriteResult, error) {
	if captured.Barrier.SessionID != plan.next.ParentSessionID {
		return nil, ErrInvalid
	}
	if _, err := CompanionsFromEvents(plan.next.ParentSessionID, captured.Events); err != nil {
		return nil, err
	}
	for _, entry := range captured.Events {
		if entry.Type != "companion/instance" {
			continue
		}
		var recorded Companion
		if json.Unmarshal(entry.Data, &recorded) != nil {
			return nil, ErrHistory
		}
		if recorded.ParentSessionID != plan.next.ParentSessionID || recorded.Name != plan.next.Name || recorded.Revision != plan.next.Revision {
			continue
		}
		actual, err := json.Marshal(recorded)
		if err != nil {
			return nil, ErrHistory
		}
		expected, err := json.Marshal(plan.next)
		if err != nil || !bytes.Equal(actual, expected) {
			return nil, ErrConflict
		}
		return &session.WriteResult{
			FirstSeq: entry.Seq,
			NextSeq:  entry.Seq + 1,
			Barrier: session.WriteBarrier{
				SessionID: plan.next.ParentSessionID,
				NextSeq:   entry.Seq + 1,
			},
			Events: []session.Event{entry},
		}, nil
	}
	return nil, nil
}
