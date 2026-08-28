package projectioncache

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sessproj "github.com/gorenx/goren/session/projection"
)

const restorationPageEvents int64 = 512

type restoredProjection struct {
	Header     session.Header
	Snapshot   sessproj.Snapshot
	Checkpoint sessproj.Checkpoint
}

// CachedSnapshot returns only version-compatible in-memory values and performs
// no Session-log or checkpoint-store I/O.
func (owner *Coordinator) CachedSnapshot(
	metadata session.Header,
) (*sessproj.Snapshot, error) {
	checkpoint, leave, err := owner.enterSession(metadata.ID)
	if err != nil {
		return nil, err
	}
	defer leave()
	record, found := checkpoint.record.snapshot()
	if !found || !identityMatchesHeader(record.Identity, metadata) {
		return nil, nil
	}
	values, err := owner.projections.ViewCheckpoint(record.Rows)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, nil
	}
	asOfSeq := int64(math.MaxInt64)
	for projectionKey := range values {
		row, rowFound := record.Rows[projectionKey]
		if !rowFound {
			return nil, fmt.Errorf(
				"session projection cache: value %q has no checkpoint row",
				projectionKey,
			)
		}
		asOfSeq = min(asOfSeq, row.Seq)
	}
	return &sessproj.Snapshot{
		AsOfSeq: asOfSeq,
		Values:  values,
	}, nil
}

// ColdSnapshot proves a current projection from durable Session facts, using a
// compatible checkpoint only to reduce the suffix that must be folded.
func (owner *Coordinator) ColdSnapshot(
	requestContext context.Context,
	identifier session.SessionID,
) (sessproj.Snapshot, error) {
	if requestContext == nil {
		return sessproj.Snapshot{}, errors.New("session projection cache: ColdSnapshot Context is nil")
	}
	if err := requestContext.Err(); err != nil {
		return sessproj.Snapshot{}, err
	}
	checkpoint, leave, err := owner.enterSession(identifier)
	if err != nil {
		return sessproj.Snapshot{}, err
	}
	defer leave()

	record, found := checkpoint.record.snapshot()
	rows := record.Rows
	floor := owner.projections.RestoreFloor(rows)
	if floor == nil {
		return owner.probeEmptySnapshot(requestContext, identifier)
	}
	restored, err := owner.restorePages(
		requestContext,
		identifier,
		rows,
		*floor,
	)
	if err != nil {
		if found {
			return owner.restoreAll(requestContext, checkpoint)
		}
		return sessproj.Snapshot{}, err
	}
	if found && !identityMatchesHeader(record.Identity, restored.Header) {
		return owner.restoreAll(requestContext, checkpoint)
	}
	owner.writeBack(requestContext, checkpoint, restored.Header, restored.Checkpoint)
	return restored.Snapshot, nil
}

func (owner *Coordinator) restoreAll(
	requestContext context.Context,
	checkpoint *sessionCache,
) (sessproj.Snapshot, error) {
	restored, err := owner.restorePages(
		requestContext,
		checkpoint.identifier,
		nil,
		0,
	)
	if err != nil {
		return sessproj.Snapshot{}, err
	}
	owner.writeBack(requestContext, checkpoint, restored.Header, restored.Checkpoint)
	return restored.Snapshot, nil
}

func (owner *Coordinator) restorePages(
	requestContext context.Context,
	identifier session.SessionID,
	checkpoint sessproj.Checkpoint,
	fromSeq int64,
) (restoredProjection, error) {
	cursor := fromSeq
	rows := checkpoint
	var metadata session.Header
	var revision sesspersist.Revision
	for {
		segment, err := owner.persistence.ReadEventsFrom(
			requestContext,
			identifier,
			sesspersist.EventContinuation{
				FromSeq: cursor,
				Limit:   restorationPageEvents,
			},
		)
		if err != nil {
			return restoredProjection{}, err
		}
		if metadata.ID == "" {
			metadata = segment.Header
			revision = segment.Revision
		} else if segment.Header.ID != metadata.ID || segment.Revision != revision {
			return restoredProjection{}, errors.New(
				"session projection cache: durable Session changed during paged restore",
			)
		}
		result, err := owner.projections.Restore(rows, segment.Events, cursor)
		if err != nil {
			return restoredProjection{}, err
		}
		rows = result.Checkpoint
		if !segment.HasMore {
			return restoredProjection{
				Header:     metadata,
				Snapshot:   result.Snapshot,
				Checkpoint: result.Checkpoint,
			}, nil
		}
		if len(segment.Events) == 0 {
			return restoredProjection{}, errors.New(
				"session projection cache: persistence returned an empty continued event page",
			)
		}
		cursor = segment.Events[len(segment.Events)-1].Seq + 1
	}
}

func (owner *Coordinator) probeEmptySnapshot(
	requestContext context.Context,
	identifier session.SessionID,
) (sessproj.Snapshot, error) {
	window, err := owner.persistence.ReadEventsBefore(
		requestContext,
		identifier,
		sesspersist.EventPage{
			Limit: 1,
		},
	)
	if err != nil {
		return sessproj.Snapshot{}, err
	}
	asOfSeq := int64(-1)
	if len(window.Events) != 0 {
		asOfSeq = window.Events[0].Seq
	}
	return sessproj.Snapshot{
		AsOfSeq: asOfSeq,
		Values:  make(sessproj.Values),
	}, nil
}

func (owner *Coordinator) writeBack(
	requestContext context.Context,
	checkpoint *sessionCache,
	metadata session.Header,
	rows sessproj.Checkpoint,
) {
	if err := checkpoint.record.replace(
		requestContext,
		CheckpointRecord{
			Identity: identityOf(metadata),
			Rows:     rows,
		},
	); err != nil {
		owner.report(Failure{
			SessionID: metadata.ID,
			Operation: "cold checkpoint write-back",
			Error:     err,
		})
	}
}

func (owner *Coordinator) replaceRecord(
	requestContext context.Context,
	identifier session.SessionID,
	record CheckpointRecord,
) error {
	checkpoint, leave, err := owner.enterSession(identifier)
	if err != nil {
		return err
	}
	defer leave()
	return checkpoint.record.replace(requestContext, record)
}
