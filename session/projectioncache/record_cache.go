package projectioncache

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/gorenx/goren/session"
)

// recordCache owns one Session ID's in-memory record and serializes its
// durable replacements. Its lock never protects writer scheduling.
type recordCache struct {
	mutex      sync.Mutex
	store      CheckpointStore
	identifier session.SessionID
	cached     CheckpointRecord
	hasRecord  bool
}

func newRecordCache(
	store CheckpointStore,
	identifier session.SessionID,
	loaded CheckpointRecord,
	hasRecord bool,
) *recordCache {
	return &recordCache{
		store:      store,
		identifier: identifier,
		cached:     loaded,
		hasRecord:  hasRecord,
	}
}

func (owner *recordCache) snapshot() (CheckpointRecord, bool) {
	owner.mutex.Lock()
	detached := cloneRecord(owner.cached)
	found := owner.hasRecord
	owner.mutex.Unlock()
	return detached, found
}

// waitForPriorReplacement makes a newly selected exact lifecycle begin after any
// older record replacement that had already crossed its epoch check.
func (owner *recordCache) waitForPriorReplacement() {
	owner.mutex.Lock()
	owner.mutex.Unlock()
}

func (owner *recordCache) replace(
	requestContext context.Context,
	candidate CheckpointRecord,
) error {
	validated, err := ValidateCheckpointRecord(owner.identifier, candidate)
	if err != nil {
		return err
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	return owner.replaceLocked(requestContext, validated)
}

func (owner *recordCache) replaceLive(
	requestContext context.Context,
	epoch uint64,
	currentEpoch *atomic.Uint64,
	candidate CheckpointRecord,
) error {
	validated, err := ValidateCheckpointRecord(owner.identifier, candidate)
	if err != nil {
		return err
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if currentEpoch.Load() != epoch {
		return nil
	}
	return owner.replaceLocked(requestContext, validated)
}

func (owner *recordCache) replaceLocked(
	requestContext context.Context,
	validated CheckpointRecord,
) error {
	if owner.hasRecord && sameIdentity(owner.cached.Identity, validated.Identity) {
		if checkpointDominates(owner.cached.Rows, validated.Rows) {
			return nil
		}
		for projectionKey, row := range owner.cached.Rows {
			if _, replaced := validated.Rows[projectionKey]; !replaced {
				validated.Rows[projectionKey] = row
			}
		}
	}
	if err := owner.store.Replace(
		requestContext,
		owner.identifier,
		validated,
	); err != nil {
		return err
	}
	owner.cached = validated
	owner.hasRecord = true
	return nil
}
