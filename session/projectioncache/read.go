package projectioncache

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

// CachedSnapshot returns only version-compatible in-memory values and performs
// no Session-log or checkpoint-store I/O.
func (owner *CheckpointCache) CachedSnapshot(
	metadata session.Header,
) (*sessionprojection.Snapshot, error) {
	owner.mutex.Lock()
	if owner.state != cacheOpen {
		owner.mutex.Unlock()
		return nil, ErrClosed
	}
	record, found := owner.records[metadata.ID]
	if found {
		record = cloneRecord(record)
	}
	owner.mutex.Unlock()
	if !found || !sameIdentity(record.Identity, identityOf(metadata)) {
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
	return &sessionprojection.Snapshot{
		AsOfSeq: asOfSeq,
		Values:  values,
	}, nil
}

// ColdSnapshot proves a current projection from durable Session facts, using a
// compatible checkpoint only to reduce the suffix that must be folded.
func (owner *CheckpointCache) ColdSnapshot(
	requestContext context.Context,
	identifier session.SessionID,
) (sessionprojection.Snapshot, error) {
	if requestContext == nil {
		return sessionprojection.Snapshot{}, errors.New("session projection cache: ColdSnapshot Context is nil")
	}
	if err := requestContext.Err(); err != nil {
		return sessionprojection.Snapshot{}, err
	}
	if err := owner.beginOperation(); err != nil {
		return sessionprojection.Snapshot{}, err
	}
	defer owner.inflight.Done()

	owner.mutex.Lock()
	record, found := owner.records[identifier]
	if found {
		record = cloneRecord(record)
	}
	owner.mutex.Unlock()
	rows := sessionprojection.Checkpoint(nil)
	if found {
		rows = record.Rows
	}
	floor := owner.projections.RestoreFloor(rows)
	if floor == nil {
		return owner.probeEmptySnapshot(requestContext, identifier)
	}
	inspection, err := owner.persistence.ReadFrom(requestContext, identifier, *floor)
	if err != nil {
		return sessionprojection.Snapshot{}, err
	}
	if !found || !sameIdentity(record.Identity, identityOf(inspection.Header)) {
		return owner.restoreAll(requestContext, identifier)
	}
	restored, err := owner.projections.Restore(rows, inspection.Events, *floor)
	if err != nil {
		return owner.restoreAll(requestContext, identifier)
	}
	owner.writeBack(requestContext, inspection.Header, restored.Checkpoint)
	return restored.Snapshot, nil
}

func (owner *CheckpointCache) restoreAll(
	requestContext context.Context,
	identifier session.SessionID,
) (sessionprojection.Snapshot, error) {
	inspection, err := owner.persistence.ReadFrom(requestContext, identifier, 0)
	if err != nil {
		return sessionprojection.Snapshot{}, err
	}
	restored, err := owner.projections.Restore(
		nil,
		inspection.Events,
		0,
	)
	if err != nil {
		return sessionprojection.Snapshot{}, err
	}
	owner.writeBack(requestContext, inspection.Header, restored.Checkpoint)
	return restored.Snapshot, nil
}

func (owner *CheckpointCache) probeEmptySnapshot(
	requestContext context.Context,
	identifier session.SessionID,
) (sessionprojection.Snapshot, error) {
	window, err := owner.persistence.ReadEventsBefore(
		requestContext,
		identifier,
		nil,
		1,
	)
	if err != nil {
		return sessionprojection.Snapshot{}, err
	}
	asOfSeq := int64(-1)
	if len(window.Events) != 0 {
		asOfSeq = window.Events[0].Seq
	}
	return sessionprojection.Snapshot{
		AsOfSeq: asOfSeq,
		Values:  make(sessionprojection.Values),
	}, nil
}

func (owner *CheckpointCache) writeBack(
	requestContext context.Context,
	metadata session.Header,
	rows sessionprojection.Checkpoint,
) {
	record := CheckpointRecord{
		Identity: identityOf(metadata),
		Rows:     rows,
	}
	if err := owner.replaceRecord(requestContext, metadata.ID, record); err != nil {
		owner.report(Failure{
			SessionID: metadata.ID,
			Operation: "cold checkpoint write-back",
			Error:     err,
		})
	}
}

func (owner *CheckpointCache) replaceRecord(
	requestContext context.Context,
	identifier session.SessionID,
	record CheckpointRecord,
) error {
	validated, err := ValidateCheckpointRecord(identifier, record)
	if err != nil {
		return err
	}
	lock := owner.recordLock(identifier)
	lock.Lock()
	defer lock.Unlock()
	owner.mutex.Lock()
	current, found := owner.records[identifier]
	if found && sameIdentity(current.Identity, validated.Identity) {
		if checkpointDominates(current.Rows, validated.Rows) {
			owner.mutex.Unlock()
			return nil
		}
		for projectionKey, row := range current.Rows {
			if _, replaced := validated.Rows[projectionKey]; !replaced {
				validated.Rows[projectionKey] = row
			}
		}
	}
	store := owner.store
	owner.mutex.Unlock()
	if err := store.Replace(requestContext, identifier, validated); err != nil {
		return err
	}
	owner.mutex.Lock()
	if owner.state == cacheOpen || owner.state == cacheClosing {
		owner.records[identifier] = cloneRecord(validated)
	}
	owner.mutex.Unlock()
	return nil
}

func checkpointDominates(
	current sessionprojection.Checkpoint,
	candidate sessionprojection.Checkpoint,
) bool {
	for projectionKey, candidateRow := range candidate {
		currentRow, found := current[projectionKey]
		if !found || currentRow.Version != candidateRow.Version ||
			currentRow.Seq < candidateRow.Seq {
			return false
		}
	}
	return true
}
