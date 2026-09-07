package projectioncache

import (
	"errors"
	"reflect"

	"github.com/gorenx/goren/session"
)

// Cache owns confirmed checkpoint records without storage or scheduling concerns.
// Its Actor owner serializes access and confirms records only after successful I/O.
type Cache struct {
	// records maps a persistent SessionID to the last confirmed checkpoint record.
	// Absence means a cache miss. Values never contain live Session or Actor references.
	records map[session.SessionID]CheckpointRecord
}

// Snapshot returns a detached confirmed record, if one exists.
func (owner *Cache) Snapshot(identifier session.SessionID) (CheckpointRecord, bool) {
	record, found := owner.records[identifier]
	if !found {
		return CheckpointRecord{}, false
	}
	return cloneRecord(record), true
}

// Match returns a confirmed record only when it belongs to the requested log.
func (owner *Cache) Match(metadata session.Header) (CheckpointRecord, bool) {
	record, found := owner.records[metadata.ID]
	if !found || !identityMatchesHeader(record.Identity, metadata) {
		return CheckpointRecord{}, false
	}
	return cloneRecord(record), true
}

// Prepare returns a detached replacement without changing the confirmed cache.
// Nil means the existing record already covers every supplied compatible row.
func (owner *Cache) Prepare(identifier session.SessionID, incoming CheckpointRecord) (*CheckpointRecord, error) {
	validated, err := ValidateCheckpointRecord(identifier, incoming)
	if err != nil {
		return nil, err
	}
	if current, found := owner.records[identifier]; found {
		var changed bool
		validated, changed = current.Merge(validated)
		if !changed {
			return nil, nil
		}
	}
	return &validated, nil
}

// Confirm installs the exact record acknowledged by storage or loaded at startup.
// A stale compatible record cannot roll back a previously confirmed Unit cursor.
func (owner *Cache) Confirm(identifier session.SessionID, persisted CheckpointRecord) error {
	validated, err := ValidateCheckpointRecord(identifier, persisted)
	if err != nil {
		return err
	}
	if current, found := owner.records[identifier]; found && sameIdentity(current.Identity, validated.Identity) {
		merged, _ := current.Merge(validated)
		if !reflect.DeepEqual(merged, validated) {
			return errors.New("session projection cache: stale checkpoint confirmation")
		}
	}
	if owner.records == nil {
		owner.records = make(map[session.SessionID]CheckpointRecord)
	}
	owner.records[identifier] = validated
	return nil
}
