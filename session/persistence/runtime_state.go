package persistence

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/session"
)

// durableSessions owns durable cursors and their optional exact live owner.
// Callers serialize mutations through sessionGates; the internal lock protects
// only membership and pointer publication.
type durableSessions struct {
	mutex   sync.Mutex
	entries map[session.SessionID]*durableState
}

func newDurableSessions() *durableSessions {
	return &durableSessions{entries: make(map[session.SessionID]*durableState)}
}

func (registry *durableSessions) Get(identifier session.SessionID) (*durableState, bool) {
	registry.mutex.Lock()
	entry, found := registry.entries[identifier]
	registry.mutex.Unlock()
	return entry, found
}

func (registry *durableSessions) Put(identifier session.SessionID, entry *durableState) {
	registry.mutex.Lock()
	registry.entries[identifier] = entry
	registry.mutex.Unlock()
}

func (registry *durableSessions) DeleteOwned(identifier session.SessionID, conversation *session.Session) {
	registry.mutex.Lock()
	if entry := registry.entries[identifier]; entry != nil && entry.owner == conversation {
		delete(registry.entries, identifier)
	}
	registry.mutex.Unlock()
}

// liveWrites owns write-behind controllers by exact live Session lifecycle.
type liveWrites struct {
	mutex   sync.Mutex
	entries map[*session.Session]*liveSessionState
}

func newLiveWrites() *liveWrites {
	return &liveWrites{entries: make(map[*session.Session]*liveSessionState)}
}

func (registry *liveWrites) Get(conversation *session.Session) (*liveSessionState, bool) {
	registry.mutex.Lock()
	entry, found := registry.entries[conversation]
	registry.mutex.Unlock()
	return entry, found
}

func (registry *liveWrites) Put(conversation *session.Session, entry *liveSessionState) {
	registry.mutex.Lock()
	registry.entries[conversation] = entry
	registry.mutex.Unlock()
}

func (registry *liveWrites) Delete(conversation *session.Session) {
	registry.mutex.Lock()
	delete(registry.entries, conversation)
	registry.mutex.Unlock()
}

func (registry *liveWrites) Snapshot() []*liveSessionState {
	registry.mutex.Lock()
	result := make([]*liveSessionState, 0, len(registry.entries))
	for _, entry := range registry.entries {
		result = append(result, entry)
	}
	registry.mutex.Unlock()
	return result
}

type serialGate struct {
	token chan struct{}
	refs  int
}

// sessionGates serializes operations for one Session identity while allowing
// unrelated Sessions to progress independently.
type sessionGates struct {
	mutex   sync.Mutex
	entries map[session.SessionID]*serialGate
	closed  bool
}

func newSessionGates() *sessionGates {
	return &sessionGates{entries: make(map[session.SessionID]*serialGate)}
}

func (registry *sessionGates) Acquire(
	requestContext context.Context,
	identifier session.SessionID,
) (func(), error) {
	if requestContext == nil {
		return nil, errors.New("session persistence: Context is nil")
	}
	registry.mutex.Lock()
	if registry.closed {
		registry.mutex.Unlock()
		return nil, errors.New("session persistence: Session Log Store is closed")
	}
	gate := registry.entries[identifier]
	if gate == nil {
		gate = &serialGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		registry.entries[identifier] = gate
	}
	gate.refs++
	registry.mutex.Unlock()
	select {
	case <-gate.token:
		return func() {
			gate.token <- struct{}{}
			registry.retire(identifier, gate)
		}, nil
	case <-requestContext.Done():
		registry.retire(identifier, gate)
		return nil, context.Cause(requestContext)
	}
}

func (registry *sessionGates) Close() {
	registry.mutex.Lock()
	registry.closed = true
	registry.mutex.Unlock()
}

func (registry *sessionGates) retire(identifier session.SessionID, gate *serialGate) {
	registry.mutex.Lock()
	gate.refs--
	if gate.refs == 0 && registry.entries[identifier] == gate {
		delete(registry.entries, identifier)
	}
	registry.mutex.Unlock()
}
