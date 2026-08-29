package projectioncache

import (
	"context"
	"sync"

	"github.com/gorenx/goren/session"
)

// checkpointRecords owns the in-memory checkpoint index and durable Store.
// Per-ID recordEntry locks allow different Session IDs to replace in parallel.
type checkpointRecords struct {
	mutex sync.RWMutex
	store CheckpointStore

	// entries maps each reusable SessionID key to its current record entry value.
	entries map[session.SessionID]*recordEntry
}

// recordEntry is one Session ID slot's record and replacement boundary. It
// never owns the Store, a live Session, or checkpoint scheduling state.
type recordEntry struct {
	mutex sync.Mutex
	// cached is nil until this Session ID has one durable reusable record.
	cached *CheckpointRecord
}

func openCheckpointRecords(
	requestContext context.Context,
	store CheckpointStore,
	failures FailureReporter,
) (*checkpointRecords, error) {
	loaded, err := store.LoadAll(requestContext)
	if err != nil {
		return nil, err
	}
	records := &checkpointRecords{
		store:   store,
		entries: make(map[session.SessionID]*recordEntry, len(loaded)),
	}
	for identifier, record := range loaded {
		validated, validationErr := ValidateCheckpointRecord(identifier, record)
		if validationErr != nil {
			reportFailure(
				failures,
				Failure{
					SessionID: identifier,
					Operation: "load checkpoint",
					Error:     validationErr,
				},
			)
			continue
		}
		records.entries[identifier] = &recordEntry{
			cached: &validated,
		}
	}
	return records, nil
}

func (records *checkpointRecords) Snapshot(
	identifier session.SessionID,
) (CheckpointRecord, bool) {
	records.mutex.RLock()
	slot := records.entries[identifier]
	records.mutex.RUnlock()
	if slot == nil {
		return CheckpointRecord{}, false
	}
	slot.mutex.Lock()
	if slot.cached == nil {
		slot.mutex.Unlock()
		return CheckpointRecord{}, false
	}
	detached := cloneRecord(*slot.cached)
	slot.mutex.Unlock()
	return detached, true
}

func (records *checkpointRecords) Replace(
	requestContext context.Context,
	identifier session.SessionID,
	candidate CheckpointRecord,
) error {
	validated, err := ValidateCheckpointRecord(identifier, candidate)
	if err != nil {
		return err
	}
	slot := records.slotFor(identifier)
	return slot.replace(requestContext, records.store, identifier, validated)
}

func (records *checkpointRecords) Close(closeContext context.Context) error {
	return records.store.Close(closeContext)
}

func (records *checkpointRecords) slotFor(identifier session.SessionID) *recordEntry {
	records.mutex.RLock()
	slot := records.entries[identifier]
	records.mutex.RUnlock()
	if slot != nil {
		return slot
	}
	records.mutex.Lock()
	slot = records.entries[identifier]
	if slot == nil {
		slot = &recordEntry{}
		records.entries[identifier] = slot
	}
	records.mutex.Unlock()
	return slot
}

func (slot *recordEntry) replace(
	requestContext context.Context,
	store CheckpointStore,
	identifier session.SessionID,
	validated CheckpointRecord,
) error {
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	if slot.cached != nil && sameIdentity(slot.cached.Identity, validated.Identity) {
		if checkpointDominates(slot.cached.Rows, validated.Rows) {
			return nil
		}
		for projectionKey, row := range slot.cached.Rows {
			if _, replaced := validated.Rows[projectionKey]; !replaced {
				validated.Rows[projectionKey] = row
			}
		}
	}
	if err := store.Replace(requestContext, identifier, validated); err != nil {
		return err
	}
	cached := cloneRecord(validated)
	slot.cached = &cached
	return nil
}
