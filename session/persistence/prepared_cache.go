package persistence

import (
	"context"
	"fmt"
	"sync"

	"github.com/gorenx/goren/session"
	lru "github.com/hashicorp/golang-lru/v2"
)

// preparedSource is one immutable cold observation and the exact unpublished
// Session reconstructed from it. revision binds the reusable object graph to
// one durable generation; repair fields remain persistence policy.
type preparedSource struct {
	inspection    Inspection
	conversation  session.Context
	revision      Revision
	marker        RepairMarker
	closers       []session.Event
	sessionLength int64
}

type reservation struct {
	pool         *preparedSessions
	conversation session.Context
	cursor       int64
	source       *preparedSource
}

func (held *reservation) Release() {
	if held == nil || held.pool == nil {
		return
	}
	held.pool.returnReservation(
		held,
		held.source.conversation.Seq() == held.source.sessionLength,
	)
}

// preparedSessions owns the bounded ready LRU and exclusive unpublished
// reservations. Reserved objects are removed from the LRU so capacity policy
// can never evict an active resume candidate.
type preparedSessions struct {
	mutex        sync.Mutex
	ready        *lru.Cache[session.SessionID, *preparedSource]
	reservations map[session.SessionID]*reservation
}

func newPreparedSessions(capacity int) (*preparedSessions, error) {
	ready, err := lru.New[session.SessionID, *preparedSource](capacity)
	if err != nil {
		return nil, err
	}
	return &preparedSessions{
		ready:        ready,
		reservations: make(map[session.SessionID]*reservation),
	}, nil
}

func (pool *preparedSessions) Has(identifier session.SessionID) bool {
	pool.mutex.Lock()
	_, reserved := pool.reservations[identifier]
	ready := pool.ready.Contains(identifier)
	pool.mutex.Unlock()
	return reserved || ready
}

func (pool *preparedSessions) Observe(
	identifier session.SessionID,
) (*preparedSource, bool, bool) {
	pool.mutex.Lock()
	if held := pool.reservations[identifier]; held != nil {
		pool.mutex.Unlock()
		return held.source, true, true
	}
	source, found := pool.ready.Get(identifier)
	pool.mutex.Unlock()
	return source, false, found
}

func (pool *preparedSessions) Store(source *preparedSource) {
	pool.mutex.Lock()
	pool.ready.Add(source.inspection.Header.ID, source)
	pool.mutex.Unlock()
}

func (pool *preparedSessions) Reserve(source *preparedSource) (*reservation, error) {
	identifier := source.inspection.Header.ID
	pool.mutex.Lock()
	defer pool.mutex.Unlock()
	if pool.reservations[identifier] != nil {
		return nil, fmt.Errorf("session persistence: session %q already has an unpublished preparation", identifier)
	}
	cached, found := pool.ready.Peek(identifier)
	if !found || cached != source {
		return nil, fmt.Errorf("session persistence: prepared source for %q is no longer ready", identifier)
	}
	pool.ready.Remove(identifier)
	held := &reservation{
		pool:         pool,
		conversation: source.conversation,
		cursor:       int64(len(source.inspection.Events)),
		source:       source,
	}
	pool.reservations[identifier] = held
	return held, nil
}

func (pool *preparedSessions) ReservationFor(
	conversation session.Context,
) (*reservation, error) {
	identifier := conversation.ID()
	pool.mutex.Lock()
	defer pool.mutex.Unlock()
	if held := pool.reservations[identifier]; held != nil {
		if held.conversation == conversation {
			return held, nil
		}
		return nil, fmt.Errorf(
			"session persistence: persisted state for %q belongs to another unpublished Session", identifier,
		)
	}
	if pool.ready.Contains(identifier) {
		return nil, fmt.Errorf(
			"session persistence: cannot publish session %q because a prepared source owns the identity", identifier,
		)
	}
	return nil, nil
}

func (pool *preparedSessions) Attach(held *reservation) error {
	identifier := held.conversation.ID()
	pool.mutex.Lock()
	defer pool.mutex.Unlock()
	if pool.reservations[identifier] != held {
		return fmt.Errorf("session persistence: session %q preparation is no longer reserved", identifier)
	}
	delete(pool.reservations, identifier)
	return nil
}

func (pool *preparedSessions) returnReservation(held *reservation, reusable bool) {
	identifier := held.conversation.ID()
	pool.mutex.Lock()
	if pool.reservations[identifier] != held {
		pool.mutex.Unlock()
		return
	}
	delete(pool.reservations, identifier)
	if reusable {
		pool.ready.Add(identifier, held.source)
	}
	pool.mutex.Unlock()
}

func (pool *preparedSessions) IsReserved(identifier session.SessionID) bool {
	pool.mutex.Lock()
	reserved := pool.reservations[identifier] != nil
	pool.mutex.Unlock()
	return reserved
}

func (pool *preparedSessions) AssertWritable(identifier session.SessionID) error {
	if pool.IsReserved(identifier) {
		return fmt.Errorf(
			"session persistence: cannot append session %q while its preparation is reserved", identifier,
		)
	}
	return nil
}

func (pool *preparedSessions) Invalidate(identifier session.SessionID, expected *preparedSource) {
	pool.mutex.Lock()
	if cached, found := pool.ready.Peek(identifier); found && (expected == nil || cached == expected) {
		pool.ready.Remove(identifier)
	}
	pool.mutex.Unlock()
}

// preparedSourceFor returns one ready or exclusively reserved observation.
// Callers hold the per-Session gate, so physical reads cannot race local
// appends. Revision verification is skipped when the caller will immediately
// perform the authoritative commit check.
func (owner *SessionLogStore) preparedSourceFor(
	requestContext context.Context,
	identifier session.SessionID,
	verifyRevision bool,
) (*preparedSource, error) {
	for {
		cached, reserved, found := owner.preparations.Observe(identifier)
		if !found {
			loaded, err := owner.loadPreparedSource(requestContext, identifier)
			if err != nil {
				return nil, err
			}
			cached = loaded
		}
		if reserved || !verifyRevision {
			return cached, nil
		}
		current, err := owner.storage.Revision(requestContext, identifier)
		if err != nil {
			return nil, err
		}
		if current != nil && *current == cached.revision {
			return cached, nil
		}
		owner.preparations.Invalidate(identifier, cached)
	}
}

func (owner *SessionLogStore) loadPreparedSource(
	requestContext context.Context,
	identifier session.SessionID,
) (*preparedSource, error) {
	stored, err := owner.storage.Load(requestContext, identifier)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, &NotFoundError{ID: identifier}
	}
	if stored.Header.ID != identifier {
		return nil, owner.corruption(identifier, fmt.Errorf(
			"stored identity mismatch: requested %q, header contains %q", identifier, stored.Header.ID,
		))
	}
	if stored.Header.Version != session.FormatVersion {
		return nil, owner.normalizeLoadError(identifier, stored.Header, fmt.Errorf(
			"unsupported Session format v%d", stored.Header.Version,
		))
	}
	for _, entry := range stored.Events {
		if !session.IsKnownEventType(entry.Type) && !entry.Ignorable {
			return nil, owner.normalizeLoadError(identifier, stored.Header, &UnsupportedFormatError{
				ID: identifier,
				Reason: fmt.Sprintf(
					"session %q contains event type %q at seq %d unknown to this harness and not marked ignorable",
					identifier, entry.Type, entry.Seq,
				),
			})
		}
	}
	closers, err := interruptedTurnClosers(stored.Events)
	if err != nil {
		return nil, owner.corruption(identifier, err)
	}
	balanced := append(snapshotEvents(stored.Events), closers...)
	conversation, err := owner.sessions.Prepare(&identifier, session.CreateOptions{
		Seed: balanced, Metadata: metadataFromHeader(stored.Header),
	})
	if err != nil {
		return nil, owner.normalizeLoadError(identifier, stored.Header, err)
	}
	if _, err := conversation.DeriveMessages(); err != nil {
		return nil, owner.normalizeLoadError(identifier, stored.Header, err)
	}
	if _, err := session.LatestRequestHeader(conversation.Events()); err != nil {
		return nil, owner.normalizeLoadError(identifier, stored.Header, err)
	}
	if _, err := session.LatestRequestContext(conversation.Events()); err != nil {
		return nil, owner.normalizeLoadError(identifier, stored.Header, err)
	}
	source := &preparedSource{
		inspection:   Inspection{Header: conversation.Header(), Events: snapshotEvents(balanced)},
		conversation: conversation, revision: stored.Revision, marker: stored.Marker,
		closers: snapshotEvents(closers), sessionLength: conversation.Seq(),
	}
	owner.preparations.Store(source)
	return source, nil
}

// commitPreparedSource verifies the cached revision before associating any
// cursor state. A repair invalidates the old revision and asks the caller to
// reload the exact committed graph.
func (owner *SessionLogStore) commitPreparedSource(
	requestContext context.Context,
	source *preparedSource,
) (bool, error) {
	identifier := source.inspection.Header.ID
	current, err := owner.storage.Revision(requestContext, identifier)
	if err != nil {
		return false, err
	}
	if current == nil || *current != source.revision {
		owner.preparations.Invalidate(identifier, source)
		return false, nil
	}
	if source.marker != nil || len(source.closers) != 0 {
		if err := owner.storage.CommitRepair(
			requestContext,
			LogRepair{
				Header:        source.inspection.Header,
				Marker:        source.marker,
				ClosingEvents: source.closers,
			},
		); err != nil {
			return false, err
		}
		owner.preparations.Invalidate(identifier, source)
		return false, nil
	}
	tracked, trackedFound := owner.durable.Get(identifier)
	if trackedFound && tracked.owner != nil {
		return false, fmt.Errorf("session persistence: session %q already has a live persistence owner", identifier)
	}
	if !trackedFound {
		tracked = &durableState{}
		owner.durable.Put(identifier, tracked)
	}
	tracked.metadata = cloneHeader(source.inspection.Header)
	tracked.cursor = int64(len(source.inspection.Events))
	tracked.materialized = true
	return true, nil
}

func cloneInspection(source Inspection) Inspection {
	return Inspection{Header: cloneHeader(source.Header), Events: snapshotEvents(source.Events)}
}
