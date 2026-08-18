package persistence

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

const (
	DefaultWriteBatchMaxDelay   = 200 * time.Millisecond
	DefaultPreparedSessionCache = 5
)

// SessionLogStoreOptions contains durable-log policy, not backend configuration.
type SessionLogStoreOptions struct {
	WriteBatchMaxDelay       time.Duration
	PreparedSessionCacheSize int
	ObserverError            func(error)
}

type durableState struct {
	metadata     session.Header
	cursor       int64
	materialized bool
	owner        *session.Session
}

type liveSessionState struct {
	conversation *session.Session
	writes       *liveWriter
}

// SessionLogStore owns the live-to-durable Session log lifecycle. It serializes
// writes per Session, validates cold logs, and applies recovery decisions while
// delegating physical storage to Backend.
type SessionLogStore struct {
	sourceScope *plugin.Scope
	sessions    session.LiveStore
	storage     Backend
	delay       time.Duration
	reporter    func(error)

	closeMutex   sync.Mutex
	closed       bool
	closing      bool
	closeDone    chan struct{}
	closeErr     error
	durable      *durableSessions
	writes       *liveWrites
	preparations *preparedSessions
	operations   *sessionGates
}

// NewSessionLogStore installs the live Session write path around a storage-only Backend.
func NewSessionLogStore(
	requestContext context.Context,
	sourceScope *plugin.Scope,
	sessions session.LiveStore,
	storage Backend,
	settings SessionLogStoreOptions,
) (*SessionLogStore, error) {
	if requestContext == nil || sourceScope == nil || sessions == nil || storage == nil {
		return nil, errors.New("session persistence: Context, Scope, Session Store, and Backend are required")
	}
	delay := settings.WriteBatchMaxDelay
	if delay == 0 {
		delay = DefaultWriteBatchMaxDelay
	}
	if delay < time.Millisecond {
		return nil, errors.New("session persistence: write batch delay must be at least one millisecond")
	}
	cacheSize := settings.PreparedSessionCacheSize
	if cacheSize == 0 {
		cacheSize = DefaultPreparedSessionCache
	}
	if cacheSize < 1 {
		return nil, errors.New("session persistence: prepared Session cache size must be positive")
	}
	preparations, err := newPreparedSessions(cacheSize)
	if err != nil {
		return nil, fmt.Errorf("session persistence: create prepared Session cache: %w", err)
	}
	reporter := settings.ObserverError
	if reporter == nil {
		reporter = func(error) {}
	}
	owner := &SessionLogStore{
		sourceScope: sourceScope, sessions: sessions, storage: storage, delay: delay, reporter: reporter,
		durable: newDurableSessions(), writes: newLiveWrites(), preparations: preparations,
		operations: newSessionGates(), closeDone: make(chan struct{}),
	}
	// Register the final drain before listeners. Scope teardown is LIFO, so
	// listener admission closes before pending writes drain and storage closes.
	if _, err := plugin.Own(sourceScope, storage.BackendName()+" write path", owner.close); err != nil {
		return nil, err
	}
	if _, err := session.OnCreated(sourceScope, owner.onCreated); err != nil {
		return nil, err
	}
	if _, err := session.OnEvent(sourceScope, owner.onEvent); err != nil {
		return nil, err
	}
	if _, err := session.OnFlush(sourceScope, owner.onFlush); err != nil {
		return nil, err
	}
	if _, err := session.OnDisposed(sourceScope, owner.onDisposed); err != nil {
		return nil, err
	}
	for _, conversation := range sessions.List() {
		if err := owner.onCreated(requestContext, conversation); err != nil {
			return nil, err
		}
	}
	return owner, requestContext.Err()
}

func (owner *SessionLogStore) Locate(metadata session.Header) (Location, bool) {
	return owner.storage.Locate(cloneHeader(metadata))
}

func (owner *SessionLogStore) SupportsRawArtifacts() bool {
	return owner.storage.SupportsRawArtifacts()
}

func (owner *SessionLogStore) ReadRaw(requestContext context.Context, identifier session.SessionID) (RawArtifact, bool, error) {
	if !owner.storage.SupportsRawArtifacts() {
		return RawArtifact{}, false, errRawArtifactsUnavailable
	}
	return owner.storage.ReadRaw(requestContext, identifier)
}

func (owner *SessionLogStore) Create(requestContext context.Context, metadata session.Header) error {
	if requestContext == nil {
		return errors.New("session persistence: create Context is nil")
	}
	metadata = cloneHeader(metadata)
	if err := validateDetachedHeader(metadata.ID, metadata); err != nil {
		return err
	}
	release, err := owner.operations.Acquire(requestContext, metadata.ID)
	if err != nil {
		return err
	}
	defer release()
	_, tracked := owner.durable.Get(metadata.ID)
	prepared := owner.preparations.Has(metadata.ID)
	if tracked || prepared {
		return fmt.Errorf("session persistence: session %q already exists", metadata.ID)
	}
	_, found, err := owner.storage.LoadStored(requestContext, metadata.ID)
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf("session persistence: session %q already has a durable log; load it instead", metadata.ID)
	}
	owner.durable.Put(metadata.ID, &durableState{metadata: metadata})
	return nil
}

func (owner *SessionLogStore) Append(requestContext context.Context, identifier session.SessionID, entries []session.Event) error {
	if requestContext == nil {
		return errors.New("session persistence: append Context is nil")
	}
	batch := snapshotEvents(entries)
	release, err := owner.operations.Acquire(requestContext, identifier)
	if err != nil {
		return err
	}
	defer release()
	return owner.appendCore(requestContext, identifier, batch)
}

func (owner *SessionLogStore) Prepare(requestContext context.Context, identifier session.SessionID) (*session.Preparation, error) {
	if requestContext == nil {
		return nil, errors.New("session persistence: prepare Context is nil")
	}
	for {
		release, err := owner.operations.Acquire(requestContext, identifier)
		if err != nil {
			return nil, err
		}
		if _, found := owner.sessions.Get(identifier); found {
			release()
			return nil, fmt.Errorf("session persistence: cannot prepare live session %q", identifier)
		}
		if owner.preparations.IsReserved(identifier) {
			release()
			return nil, fmt.Errorf("session persistence: session %q already has an unpublished preparation", identifier)
		}
		source, err := owner.preparedSourceFor(requestContext, identifier, false)
		if err != nil {
			release()
			return nil, err
		}
		stable, err := owner.commitPreparedSource(requestContext, source)
		if err != nil {
			release()
			return nil, err
		}
		if !stable {
			release()
			continue
		}
		reservation, err := owner.preparations.Reserve(source)
		if err != nil {
			release()
			return nil, err
		}
		release()
		return session.NewPreparation(source.conversation, func() {
			owner.preparations.ReturnReservation(
				reservation, source.conversation.Seq() == source.sessionLength,
			)
		}), nil
	}
}

func (owner *SessionLogStore) Load(requestContext context.Context, identifier session.SessionID) (Inspection, error) {
	if requestContext == nil {
		return Inspection{}, errors.New("session persistence: load Context is nil")
	}
	if conversation, found := owner.sessions.Get(identifier); found {
		if err := owner.onFlush(requestContext, conversation); err != nil {
			return Inspection{}, err
		}
		entries := conversation.Events()
		if len(entries) == 0 {
			return Inspection{}, &NotFoundError{ID: identifier}
		}
		closers, err := interruptedTurnClosers(entries)
		if err != nil {
			return Inspection{}, err
		}
		if len(closers) != 0 {
			return Inspection{}, fmt.Errorf("session persistence: cannot load live session %q while its turn is open", identifier)
		}
		return Inspection{Header: conversation.Header(), Events: entries}, nil
	}
	for {
		release, err := owner.operations.Acquire(requestContext, identifier)
		if err != nil {
			return Inspection{}, err
		}
		source, err := owner.preparedSourceFor(requestContext, identifier, false)
		if err != nil {
			release()
			return Inspection{}, err
		}
		stable, err := owner.commitPreparedSource(requestContext, source)
		release()
		if err != nil {
			return Inspection{}, err
		}
		if stable {
			owner.preparations.Invalidate(identifier, source)
			return cloneInspection(source.inspection), nil
		}
	}
}

func (owner *SessionLogStore) Inspect(requestContext context.Context, identifier session.SessionID) (Inspection, error) {
	if requestContext == nil {
		return Inspection{}, errors.New("session persistence: inspect Context is nil")
	}
	if conversation, found := owner.sessions.Get(identifier); found {
		return Inspection{Header: conversation.Header(), Events: conversation.Events()}, nil
	}
	release, err := owner.operations.Acquire(requestContext, identifier)
	if err != nil {
		return Inspection{}, err
	}
	defer release()
	source, err := owner.preparedSourceFor(requestContext, identifier, true)
	if err != nil {
		return Inspection{}, err
	}
	return cloneInspection(source.inspection), nil
}

func (owner *SessionLogStore) ReadFrom(requestContext context.Context, identifier session.SessionID, fromSeq int64) (Inspection, error) {
	if requestContext == nil {
		return Inspection{}, errors.New("session persistence: readFrom Context is nil")
	}
	if fromSeq < 0 {
		return Inspection{}, errors.New("session persistence: fromSeq must be non-negative")
	}
	release, err := owner.operations.Acquire(requestContext, identifier)
	if err != nil {
		return Inspection{}, err
	}
	defer release()
	stored, found, err := owner.storage.LoadStoredFrom(requestContext, identifier, fromSeq)
	if err != nil {
		return Inspection{}, err
	}
	if !found {
		return Inspection{}, &NotFoundError{ID: identifier}
	}
	if stored.Header.ID != identifier {
		return Inspection{}, owner.corruption(identifier, fmt.Errorf(
			"stored identity mismatch: requested %q, header contains %q", identifier, stored.Header.ID,
		))
	}
	if stored.Header.Version != session.FormatVersion {
		return Inspection{}, owner.normalizeLoadError(identifier, stored.Header, fmt.Errorf(
			"unsupported Session format v%d", stored.Header.Version,
		))
	}
	for position, entry := range stored.Events {
		expected := fromSeq + int64(position)
		if entry.Seq != expected {
			return Inspection{}, owner.corruption(identifier, fmt.Errorf(
				"stored suffix seq mismatch: expected %d, got %d", expected, entry.Seq,
			))
		}
		if !session.IsKnownEventType(entry.Type) && !entry.Ignorable {
			return Inspection{}, owner.normalizeLoadError(identifier, stored.Header, &UnsupportedFormatError{
				ID: identifier,
				Reason: fmt.Sprintf(
					"session %q contains event type %q at seq %d unknown to this harness and not marked ignorable",
					identifier, entry.Type, entry.Seq,
				),
			})
		}
	}
	return Inspection{Header: cloneHeader(stored.Header), Events: snapshotEvents(stored.Events)}, nil
}

func (owner *SessionLogStore) List(requestContext context.Context) ([]session.Header, error) {
	return owner.storage.ListStored(requestContext)
}

func (owner *SessionLogStore) ListSnapshots(requestContext context.Context) ([]Snapshot, error) {
	return owner.storage.ListStoredSnapshots(requestContext)
}

func (owner *SessionLogStore) onCreated(requestContext context.Context, conversation *session.Session) error {
	if conversation == nil {
		return errors.New("session persistence: created Session is nil")
	}
	identifier := conversation.ID()
	release, err := owner.operations.Acquire(requestContext, identifier)
	if err != nil {
		return err
	}
	defer release()
	seed := conversation.Events()
	metadata := conversation.Header()

	reservation, err := owner.preparations.ReservationFor(conversation)
	if err != nil {
		return err
	}
	tracked, trackedFound := owner.durable.Get(identifier)
	if reservation != nil {
		if err := owner.preparations.Attach(reservation); err != nil {
			return err
		}
		if !trackedFound {
			tracked = &durableState{metadata: metadata, cursor: reservation.cursor, materialized: true}
			owner.durable.Put(identifier, tracked)
		}
		tracked.owner = conversation
		if err := owner.appendCore(requestContext, identifier, seed[reservation.cursor:]); err != nil {
			return err
		}
		owner.installLive(conversation)
		return nil
	}
	if trackedFound {
		if tracked.owner != nil && tracked.owner != conversation {
			return fmt.Errorf("session persistence: session %q already has a live persistence owner", identifier)
		}
		if tracked.metadata.CWD != nil || metadata.CWD != nil {
			if !sameText(tracked.metadata.CWD, metadata.CWD) {
				return fmt.Errorf("session persistence: session %q is already persisted at a different cwd", identifier)
			}
		}
		if tracked.cursor > int64(len(seed)) {
			return fmt.Errorf("session persistence: live seed for %q is shorter than its durable prefix", identifier)
		}
		if tracked.cursor != 0 {
			stored, found, err := owner.storage.LoadStored(requestContext, identifier)
			if err != nil {
				return err
			}
			if !found || tracked.cursor > int64(len(stored.Events)) ||
				!eventPrefixesEqual(seed, stored.Events[:int(tracked.cursor)]) {
				return fmt.Errorf("session persistence: live seed for %q does not match its durable prefix", identifier)
			}
		}
		tracked.owner = conversation
		if err := owner.appendCore(requestContext, identifier, seed[tracked.cursor:]); err != nil {
			return err
		}
		owner.installLive(conversation)
		return nil
	}

	stored, found, err := owner.storage.LoadStored(requestContext, identifier)
	if err != nil {
		return err
	}
	if found {
		loaded, err := owner.logicalInspection(identifier, stored)
		if err != nil {
			return err
		}
		if !sameText(loaded.Header.CWD, metadata.CWD) || !eventPrefixesEqual(seed, loaded.Events) {
			return fmt.Errorf("session persistence: session %q collides with a different durable log", identifier)
		}
		if stored.Marker != nil {
			if err := owner.storage.CommitRepair(requestContext, loaded.Header, stored.Marker, nil); err != nil {
				return err
			}
		}
		tracked = &durableState{
			metadata: loaded.Header, cursor: int64(len(loaded.Events)), materialized: true, owner: conversation,
		}
		owner.durable.Put(identifier, tracked)
		if err := owner.appendCore(requestContext, identifier, seed[tracked.cursor:]); err != nil {
			return err
		}
		owner.installLive(conversation)
		return nil
	}

	tracked = &durableState{metadata: metadata, owner: conversation}
	owner.durable.Put(identifier, tracked)
	if err := owner.appendCore(requestContext, identifier, seed); err != nil {
		return err
	}
	owner.installLive(conversation)
	return nil
}

func (owner *SessionLogStore) onEvent(_ context.Context, conversation *session.Session, committed session.Event) error {
	live, found := owner.writes.Get(conversation)
	if !found {
		return fmt.Errorf("session persistence: event for uninitialized session %q", conversation.ID())
	}
	live.writes.enqueue(committed)
	return nil
}

func (owner *SessionLogStore) onFlush(requestContext context.Context, conversation *session.Session) error {
	live, found := owner.writes.Get(conversation)
	if !found {
		return fmt.Errorf("session persistence: flush for uninitialized session %q", conversation.ID())
	}
	return live.writes.flush(requestContext)
}

func (owner *SessionLogStore) onDisposed(requestContext context.Context, conversation *session.Session) error {
	live, found := owner.writes.Get(conversation)
	if !found {
		return nil
	}
	if err := live.writes.flush(requestContext); err != nil {
		return err
	}
	release, err := owner.operations.Acquire(requestContext, conversation.ID())
	if err != nil {
		return err
	}
	owner.writes.Delete(conversation)
	owner.durable.DeleteOwned(conversation.ID(), conversation)
	release()
	return nil
}

func (owner *SessionLogStore) installLive(conversation *session.Session) {
	identifier := conversation.ID()
	writes := newLiveWriter(owner.delay, func(requestContext context.Context, entries []session.Event) error {
		release, err := owner.operations.Acquire(requestContext, identifier)
		if err != nil {
			return err
		}
		defer release()
		return owner.appendLiveBatch(requestContext, identifier, entries)
	}, func(problem error) {
		owner.safeReport(fmt.Errorf("%s: background write for session %q failed; events retained: %w", owner.storage.BackendName(), identifier, problem))
	})
	owner.writes.Put(conversation, &liveSessionState{conversation: conversation, writes: writes})
}

func (owner *SessionLogStore) appendLiveBatch(requestContext context.Context, identifier session.SessionID, entries []session.Event) error {
	tracked, found := owner.durable.Get(identifier)
	if !found {
		return fmt.Errorf("session persistence: session %q lost its durable cursor", identifier)
	}
	fresh := make([]session.Event, 0, len(entries))
	for _, entry := range entries {
		if entry.Seq >= tracked.cursor {
			fresh = append(fresh, entry)
		}
	}
	return owner.appendCore(requestContext, identifier, fresh)
}

func (owner *SessionLogStore) appendCore(requestContext context.Context, identifier session.SessionID, entries []session.Event) error {
	if len(entries) == 0 {
		return nil
	}
	if err := owner.preparations.AssertWritable(identifier); err != nil {
		return err
	}
	tracked, trackedFound := owner.durable.Get(identifier)
	for !trackedFound {
		stored, found, err := owner.storage.LoadStored(requestContext, identifier)
		if err != nil {
			return err
		}
		if !found {
			return &NotFoundError{ID: identifier}
		}
		loaded, stable, err := owner.commitCold(requestContext, identifier, stored)
		if err != nil {
			return err
		}
		if !stable {
			continue
		}
		tracked, trackedFound = owner.durable.Get(identifier)
		if !trackedFound {
			tracked = &durableState{metadata: loaded.Header, cursor: int64(len(loaded.Events)), materialized: true}
			owner.durable.Put(identifier, tracked)
			trackedFound = true
		}
	}
	for index, entry := range entries {
		expected := tracked.cursor + int64(index)
		if entry.Seq != expected {
			return fmt.Errorf("session persistence: append seq mismatch for %q: expected %d at index %d, got %d", identifier, expected, index, entry.Seq)
		}
	}
	if err := owner.storage.AppendBatch(requestContext, tracked.metadata, snapshotEvents(entries), tracked.materialized); err != nil {
		return err
	}
	tracked.materialized = true
	tracked.cursor += int64(len(entries))
	owner.preparations.Invalidate(identifier, nil)
	return nil
}

func (owner *SessionLogStore) commitCold(
	requestContext context.Context,
	identifier session.SessionID,
	stored StoredPrefix,
) (Inspection, bool, error) {
	loaded, err := owner.logicalInspection(identifier, stored)
	if err != nil {
		return Inspection{}, false, err
	}
	closers, err := interruptedTurnClosers(loaded.Events)
	if err != nil {
		return Inspection{}, false, owner.corruption(identifier, err)
	}
	loaded.Events = append(loaded.Events, closers...)
	if _, err := inspectStored(loaded.Header, loaded.Events); err != nil {
		return Inspection{}, false, owner.normalizeLoadError(identifier, loaded.Header, err)
	}
	current, found, err := owner.storage.ReadStoredRevision(requestContext, identifier)
	if err != nil {
		return Inspection{}, false, err
	}
	if !found || current != stored.Token {
		return Inspection{}, false, nil
	}
	if stored.Marker != nil || len(closers) != 0 {
		if err := owner.storage.CommitRepair(requestContext, loaded.Header, stored.Marker, closers); err != nil {
			return Inspection{}, false, err
		}
		return Inspection{}, false, nil
	}
	tracked, found := owner.durable.Get(identifier)
	if !found || tracked.owner == nil {
		owner.durable.Put(identifier, &durableState{
			metadata: loaded.Header, cursor: int64(len(loaded.Events)), materialized: true,
		})
	}
	return loaded, true, nil
}

func (owner *SessionLogStore) logicalInspection(identifier session.SessionID, stored StoredPrefix) (Inspection, error) {
	if stored.Header.ID != identifier {
		return Inspection{}, owner.corruption(identifier, fmt.Errorf(
			"stored identity mismatch: requested %q, header contains %q", identifier, stored.Header.ID,
		))
	}
	loaded, err := inspectStored(stored.Header, stored.Events)
	if err != nil {
		return Inspection{}, owner.normalizeLoadError(identifier, stored.Header, err)
	}
	return loaded, nil
}

func (owner *SessionLogStore) normalizeLoadError(identifier session.SessionID, metadata session.Header, problem error) error {
	var unsupported *UnsupportedFormatError
	if errors.As(problem, &unsupported) {
		if unsupported.Location == nil {
			if artifactLocation, found := owner.storage.Locate(metadata); found {
				unsupported.Location = &artifactLocation
			}
		}
		return unsupported
	}
	if metadata.Version != session.FormatVersion {
		reason := formatVersionRefusal(identifier, metadata.Version)
		refusal := &UnsupportedFormatError{ID: identifier, Reason: reason}
		if artifactLocation, found := owner.storage.Locate(metadata); found {
			refusal.Location = &artifactLocation
		}
		return refusal
	}
	return owner.corruption(identifier, problem)
}

func (owner *SessionLogStore) corruption(identifier session.SessionID, problem error) error {
	var storedFailure *CorruptionError
	if errors.As(problem, &storedFailure) {
		return problem
	}
	return &CorruptionError{ID: identifier, Cause: problem}
}

func (owner *SessionLogStore) close(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	owner.closeMutex.Lock()
	if owner.closed {
		closeErr := owner.closeErr
		owner.closeMutex.Unlock()
		return closeErr
	}
	if owner.closing {
		done := owner.closeDone
		owner.closeMutex.Unlock()
		select {
		case <-done:
			owner.closeMutex.Lock()
			closeErr := owner.closeErr
			owner.closeMutex.Unlock()
			return closeErr
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	}
	owner.closing = true
	liveStates := owner.writes.Snapshot()
	owner.closeMutex.Unlock()
	var closeErr error
	for _, live := range liveStates {
		closeErr = errors.Join(closeErr, live.writes.flush(closeContext))
	}
	owner.operations.Close()
	closeErr = errors.Join(closeErr, owner.storage.Close(closeContext))
	owner.closeMutex.Lock()
	owner.closeErr = closeErr
	owner.closed = true
	owner.closing = false
	close(owner.closeDone)
	owner.closeMutex.Unlock()
	return closeErr
}

func (owner *SessionLogStore) safeReport(problem error) {
	defer func() { _ = recover() }()
	owner.reporter(problem)
}

func validateDetachedHeader(identifier session.SessionID, metadata session.Header) error {
	conversation, err := session.New(identifier, session.CreateOptions{Metadata: metadataFromHeader(metadata)})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(conversation.Header(), metadata) {
		return errors.New("session persistence: header is not canonical")
	}
	return nil
}

func metadataFromHeader(metadata session.Header) session.Metadata {
	return session.Metadata{
		CreatedAt: int64Pointer(metadata.CreatedAt), CWD: cloneTextPointer(metadata.CWD),
		ParentSession: cloneSessionPointer(metadata.ParentSession), SeedLength: cloneInt64Pointer(metadata.SeedLength),
		Origin: metadata.Origin, DelegationDepth: cloneInt64Pointer(metadata.DelegationDepth),
		AgentPreset: cloneTextPointer(metadata.AgentPreset),
	}
}

func formatVersionRefusal(identifier session.SessionID, version int) string {
	if version > session.FormatVersion {
		return fmt.Sprintf(
			"session %q uses log format v%d, but this harness reads only v%d; upgrade the harness to open it",
			identifier, version, session.FormatVersion,
		)
	}
	return fmt.Sprintf(
		"session %q uses log format v%d, older than supported v%d, and this build ships no upgrade path",
		identifier, version, session.FormatVersion,
	)
}

func sameText(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
