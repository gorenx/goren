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

// restorationPageEvents bounds each durable suffix page folded during cold restore.
const restorationPageEvents int64 = 512

type restoredProjection struct {
	Header     session.Header
	Snapshot   sessproj.Snapshot
	Checkpoint sessproj.Checkpoint
}

// snapshotReader owns cached and durable Projection snapshot reconstruction.
// It performs no lifecycle admission and owns no record or live-write state.
type snapshotReader struct {
	persistence DurableEventReader
	projections CheckpointProjector
	records     *checkpointRecords
	failures    FailureReporter
}

func newSnapshotReader(
	persistence DurableEventReader,
	projections CheckpointProjector,
	records *checkpointRecords,
	failures FailureReporter,
) *snapshotReader {
	return &snapshotReader{
		persistence: persistence,
		projections: projections,
		records:     records,
		failures:    failures,
	}
}

// CachedSnapshot returns only version-compatible in-memory values and performs
// no Session-log or checkpoint-store I/O.
func (reader *snapshotReader) CachedSnapshot(
	metadata session.Header,
) (*sessproj.Snapshot, error) {
	record, found := reader.records.Snapshot(metadata.ID)
	if !found || !identityMatchesHeader(record.Identity, metadata) {
		return nil, nil
	}
	values, err := reader.projections.ViewCheckpoint(record.Rows)
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
func (reader *snapshotReader) ColdSnapshot(
	requestContext context.Context,
	identifier session.SessionID,
) (sessproj.Snapshot, error) {
	if requestContext == nil {
		return sessproj.Snapshot{}, errors.New("session projection cache: ColdSnapshot Context is nil")
	}
	if err := requestContext.Err(); err != nil {
		return sessproj.Snapshot{}, err
	}
	record, found := reader.records.Snapshot(identifier)
	rows := record.Rows
	floor := reader.projections.RestoreFloor(rows)
	if floor == nil {
		return reader.probeEmptySnapshot(requestContext, identifier)
	}
	restored, err := reader.restorePages(
		requestContext,
		identifier,
		rows,
		*floor,
	)
	if err != nil {
		if found {
			return reader.restoreAll(requestContext, identifier)
		}
		return sessproj.Snapshot{}, err
	}
	if found && !identityMatchesHeader(record.Identity, restored.Header) {
		return reader.restoreAll(requestContext, identifier)
	}
	reader.writeBack(requestContext, restored.Header, restored.Checkpoint)
	return restored.Snapshot, nil
}

func (reader *snapshotReader) restoreAll(
	requestContext context.Context,
	identifier session.SessionID,
) (sessproj.Snapshot, error) {
	restored, err := reader.restorePages(requestContext, identifier, nil, 0)
	if err != nil {
		return sessproj.Snapshot{}, err
	}
	reader.writeBack(requestContext, restored.Header, restored.Checkpoint)
	return restored.Snapshot, nil
}

func (reader *snapshotReader) restorePages(
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
		segment, err := reader.persistence.ReadEventsFrom(
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
		result, err := reader.projections.Restore(rows, segment.Events, cursor)
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

func (reader *snapshotReader) probeEmptySnapshot(
	requestContext context.Context,
	identifier session.SessionID,
) (sessproj.Snapshot, error) {
	window, err := reader.persistence.ReadEventsBefore(
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

func (reader *snapshotReader) writeBack(
	requestContext context.Context,
	metadata session.Header,
	rows sessproj.Checkpoint,
) {
	if err := reader.records.Replace(
		requestContext,
		metadata.ID,
		CheckpointRecord{
			Identity: identityOf(metadata),
			Rows:     rows,
		},
	); err != nil {
		reportFailure(
			reader.failures,
			Failure{
				SessionID: metadata.ID,
				Operation: "cold checkpoint write-back",
				Error:     err,
			},
		)
	}
}
