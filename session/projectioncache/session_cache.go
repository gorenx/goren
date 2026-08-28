package projectioncache

import (
	"sync"
	"sync/atomic"

	"github.com/gorenx/goren/session"
)

// sessionCache coordinates the exact lifecycle writers for one Session ID.
// Persisted record state belongs to its recordCache collaborator.
type sessionCache struct {
	coordinator  *Coordinator
	identifier   session.SessionID
	record       *recordCache
	writersMutex sync.Mutex
	writers      map[session.Context]*checkpointWriter
	nextEpoch    uint64
	currentEpoch atomic.Uint64
}

func newSessionCache(
	cacheCoordinator *Coordinator,
	identifier session.SessionID,
	loaded CheckpointRecord,
	hasRecord bool,
) *sessionCache {
	return &sessionCache{
		coordinator: cacheCoordinator,
		identifier:  identifier,
		record:      newRecordCache(cacheCoordinator.store, identifier, loaded, hasRecord),
		writers:     make(map[session.Context]*checkpointWriter),
	}
}

func (owner *sessionCache) writerFor(
	conversation session.Context,
) *checkpointWriter {
	owner.writersMutex.Lock()
	selected := owner.writers[conversation]
	if selected != nil {
		ready := selected.ready
		owner.writersMutex.Unlock()
		<-ready
		return selected
	}
	owner.nextEpoch++
	selected = newCheckpointWriter(owner, conversation, owner.nextEpoch)
	owner.writers[conversation] = selected
	owner.writersMutex.Unlock()
	owner.currentEpoch.Store(selected.epoch)
	owner.record.waitForPriorReplacement()
	close(selected.ready)
	return selected
}

func (owner *sessionCache) isCurrent(writer *checkpointWriter) bool {
	owner.writersMutex.Lock()
	current := owner.writers[writer.conversation] == writer &&
		owner.currentEpoch.Load() == writer.epoch
	owner.writersMutex.Unlock()
	return current
}

func (owner *sessionCache) removeWriter(writer *checkpointWriter) {
	owner.writersMutex.Lock()
	if owner.writers[writer.conversation] == writer {
		delete(owner.writers, writer.conversation)
	}
	owner.writersMutex.Unlock()
}

func (owner *sessionCache) stop() {
	owner.writersMutex.Lock()
	writers := make([]*checkpointWriter, 0, len(owner.writers))
	for _, writer := range owner.writers {
		writers = append(writers, writer)
	}
	owner.writersMutex.Unlock()
	for _, writer := range writers {
		writer.stop()
	}
}
