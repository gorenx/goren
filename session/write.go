package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// ErrWritesClosed reports that the Session no longer admits writes.
var ErrWritesClosed = errors.New("session: writes are closed")

// WritePlan builds one complete atomic event batch from the Session snapshot
// supplied by its serialized owner. Build must be deterministic,
// must not perform external I/O, and must not retain or mutate the snapshot.
// A plan that does not depend on current state should be created with Batch.
type WritePlan interface {
	Build(context.Context, Snapshot) ([]EventDraft, error)
}

// Batch snapshots a fixed event batch for Commit. The drafts retain their
// slice order and cannot be mutated through caller-owned payload aliases.
func Batch(drafts ...EventDraft) WritePlan {
	detached := make([]EventDraft, len(drafts))
	for index, draft := range drafts {
		detached[index] = cloneEventDraft(draft)
	}
	return fixedWritePlan{
		drafts: detached,
	}
}

// EventDraft is an immutable, not-yet-sequenced event candidate. Its fields
// stay private so only owner-created EventKey values establish event identity.
type EventDraft struct {
	eventType        string
	data             json.RawMessage
	sourceEventSeqs  *[]int64
	surfaceOperation *SurfaceOperation
	ignorable        bool
}

// CheckCommit verifies the exact event returned by committing this immutable draft.
// It compares payload bytes preserved by Log.Commit, not arbitrary historical JSON
// encodings. The caller separately checks Session identity and the requested range.
func (draft EventDraft) CheckCommit(entry Event) error {
	if err := validateEventDraft(draft); err != nil {
		return err
	}
	if !isSafeNonNegative(entry.Seq) || !isSafeInteger(entry.Time) {
		return errors.New("session: committed event has invalid sequence or time")
	}
	if entry.Type != draft.eventType || !bytes.Equal(entry.Data, draft.data) ||
		entry.Ignorable != draft.ignorable ||
		!reflect.DeepEqual(entry.SourceEventSeqs, draft.sourceEventSeqs) ||
		!reflect.DeepEqual(entry.SurfaceOp, draft.surfaceOperation) {
		return errors.New("session: committed event does not match its draft")
	}
	return nil
}

// NewEventDraft snapshots one typed non-surface event before queue admission.
func NewEventDraft[D any](
	definition EventKey[D],
	payload D,
	options ...EventOptions,
) (EventDraft, error) {
	if definition.name == "" {
		return EventDraft{}, errors.New("session: draft has invalid event key")
	}
	settings, err := oneEventOptions(options)
	if err != nil {
		return EventDraft{}, err
	}
	rawValue, err := snapshotPayload(payload)
	if err != nil {
		return EventDraft{}, err
	}
	return EventDraft{
		eventType: definition.name,
		data:      rawValue,
		ignorable: settings.Ignorable,
	}, nil
}

// NewSurfaceEventDraft snapshots one typed surface event before queue admission.
func NewSurfaceEventDraft[D any](
	definition SurfaceEventKey[D],
	payload D,
	intent SurfaceIntent,
) (EventDraft, error) {
	if definition.name == "" {
		return EventDraft{}, errors.New("session: draft has invalid surface event key")
	}
	if _, err := json.Marshal(intent.Operation); err != nil {
		return EventDraft{}, err
	}
	rawValue, err := snapshotPayload(payload)
	if err != nil {
		return EventDraft{}, err
	}
	operation := intent.Operation
	return EventDraft{
		eventType:        definition.name,
		data:             rawValue,
		sourceEventSeqs:  cloneSequences(intent.SourceEventSeqs),
		surfaceOperation: &operation,
		ignorable:        intent.Ignorable,
	}, nil
}

// WriteBarrier identifies a committed Session prefix. Persistence satisfies
// the barrier only after every event with Seq less than NextSeq is durable.
type WriteBarrier struct {
	SessionID SessionID
	NextSeq   int64
}

// Snapshot is one atomic committed Session revision. Surface is the
// model-visible view of Events, and Barrier identifies the same prefix.
type Snapshot struct {
	Events  []Event
	Surface Surface
	Barrier WriteBarrier
}

// WriteResult describes the batch committed by one FIFO request. The range is
// half-open: [FirstSeq, NextSeq). A no-op plan has equal bounds.
type WriteResult struct {
	FirstSeq int64
	NextSeq  int64
	Barrier  WriteBarrier
	Events   []Event
}

func validateEventDraft(draft EventDraft) error {
	if draft.eventType == "" {
		return errors.New("session: invalid EventDraft")
	}
	if len(draft.data) == 0 {
		return errors.New("session: EventDraft data is absent")
	}
	if err := validateLosslessJSON(draft.data); err != nil {
		return fmt.Errorf("session: EventDraft data is not lossless JSON: %w", err)
	}
	return nil
}

func cloneEventDraft(source EventDraft) EventDraft {
	detached := source
	detached.data = append(json.RawMessage(nil), source.data...)
	detached.sourceEventSeqs = cloneSequences(source.sourceEventSeqs)
	if source.surfaceOperation != nil {
		operation := *source.surfaceOperation
		detached.surfaceOperation = &operation
	}
	return detached
}

type fixedWritePlan struct {
	drafts []EventDraft
}

func (plan fixedWritePlan) Build(context.Context, Snapshot) ([]EventDraft, error) {
	if len(plan.drafts) == 0 {
		return nil, errors.New("session: Batch requires at least one EventDraft")
	}
	return append([]EventDraft(nil), plan.drafts...), nil
}

func prepareEventDrafts(drafts []EventDraft) ([]EventDraft, error) {
	prepared := make([]EventDraft, len(drafts))
	for index, draft := range drafts {
		if err := validateEventDraft(draft); err != nil {
			return nil, fmt.Errorf("session: invalid EventDraft at index %d: %w", index, err)
		}
		prepared[index] = cloneEventDraft(draft)
	}
	return prepared, nil
}

func contextCause(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("session: Context is nil")
	}
	if requestContext.Err() == nil {
		return nil
	}
	return context.Cause(requestContext)
}
