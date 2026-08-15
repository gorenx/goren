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

const DefaultWriteBatchMaxDelay = 200 * time.Millisecond

// CoordinatorOptions contains orchestration policy, not backend configuration.
type CoordinatorOptions struct {
	WriteBatchMaxDelay time.Duration
	ObserverError      func(error)
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

type preparedReservation struct {
	conversation *session.Session
	cursor       int64
}

type serialGate struct {
	token chan struct{}
	refs  int
}

// Coordinator supplies backend-neutral persistence policy over one storage adapter.
type Coordinator struct {
	sourceScope *plugin.Scope
	sessions    session.Store
	storage     Backend
	delay       time.Duration
	reporter    func(error)

	mutex        sync.Mutex
	closed       bool
	closing      bool
	closeDone    chan struct{}
	closeErr     error
	states       map[session.SessionID]*durableState
	live         map[*session.Session]*liveSessionState
	reservations map[session.SessionID]*preparedReservation
	gates        map[session.SessionID]*serialGate
}

// NewCoordinator installs the live Session write path around a storage-only backend.
func NewCoordinator(
	requestContext context.Context,
	sourceScope *plugin.Scope,
	sessions session.Store,
	storage Backend,
	settings CoordinatorOptions,
) (*Coordinator, error) {
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
	reporter := settings.ObserverError
	if reporter == nil {
		reporter = func(error) {}
	}
	owner := &Coordinator{
		sourceScope: sourceScope, sessions: sessions, storage: storage, delay: delay, reporter: reporter,
		states: make(map[session.SessionID]*durableState), live: make(map[*session.Session]*liveSessionState),
		reservations: make(map[session.SessionID]*preparedReservation), gates: make(map[session.SessionID]*serialGate),
		closeDone: make(chan struct{}),
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

func (owner *Coordinator) Locate(metadata session.Header) (Location, bool) {
	return owner.storage.Locate(cloneHeader(metadata))
}

func (owner *Coordinator) SupportsRawArtifacts() bool {
	return owner.storage.SupportsRawArtifacts()
}

func (owner *Coordinator) ReadRaw(requestContext context.Context, identifier session.SessionID) (RawArtifact, bool, error) {
	if !owner.storage.SupportsRawArtifacts() {
		return RawArtifact{}, false, errRawArtifactsUnavailable
	}
	return owner.storage.ReadRaw(requestContext, identifier)
}

func (owner *Coordinator) Create(requestContext context.Context, metadata session.Header) error {
	if requestContext == nil {
		return errors.New("session persistence: create Context is nil")
	}
	metadata = cloneHeader(metadata)
	if err := validateDetachedHeader(metadata.ID, metadata); err != nil {
		return err
	}
	release, err := owner.acquire(requestContext, metadata.ID)
	if err != nil {
		return err
	}
	defer release()
	owner.mutex.Lock()
	_, tracked := owner.states[metadata.ID]
	_, reserved := owner.reservations[metadata.ID]
	owner.mutex.Unlock()
	if tracked || reserved {
		return fmt.Errorf("session persistence: session %q already exists", metadata.ID)
	}
	_, found, err := owner.storage.LoadStored(requestContext, metadata.ID)
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf("session persistence: session %q already has a durable log; load it instead", metadata.ID)
	}
	owner.mutex.Lock()
	owner.states[metadata.ID] = &durableState{metadata: metadata}
	owner.mutex.Unlock()
	return nil
}

func (owner *Coordinator) Append(requestContext context.Context, identifier session.SessionID, entries []session.Event) error {
	if requestContext == nil {
		return errors.New("session persistence: append Context is nil")
	}
	batch := snapshotEvents(entries)
	release, err := owner.acquire(requestContext, identifier)
	if err != nil {
		return err
	}
	defer release()
	return owner.appendCore(requestContext, identifier, batch)
}

func (owner *Coordinator) Prepare(requestContext context.Context, identifier session.SessionID) (*session.Preparation, error) {
	if requestContext == nil {
		return nil, errors.New("session persistence: prepare Context is nil")
	}
	for {
		release, err := owner.acquire(requestContext, identifier)
		if err != nil {
			return nil, err
		}
		if _, found := owner.sessions.Get(identifier); found {
			release()
			return nil, fmt.Errorf("session persistence: cannot prepare live session %q", identifier)
		}
		owner.mutex.Lock()
		_, alreadyReserved := owner.reservations[identifier]
		owner.mutex.Unlock()
		if alreadyReserved {
			release()
			return nil, fmt.Errorf("session persistence: session %q already has an unpublished preparation", identifier)
		}
		loaded, stable, err := owner.loadCold(requestContext, identifier, true)
		if err != nil {
			release()
			return nil, err
		}
		if !stable {
			release()
			continue
		}
		metadata := metadataFromHeader(loaded.Header)
		conversation, err := owner.sessions.Prepare(&identifier, session.CreateOptions{
			Seed: loaded.Events, Metadata: metadata,
		})
		if err != nil {
			release()
			return nil, err
		}
		reservation := &preparedReservation{conversation: conversation, cursor: int64(len(loaded.Events))}
		owner.mutex.Lock()
		owner.reservations[identifier] = reservation
		owner.mutex.Unlock()
		release()
		return session.NewPreparation(conversation, func() {
			owner.mutex.Lock()
			if owner.reservations[identifier] == reservation {
				delete(owner.reservations, identifier)
			}
			owner.mutex.Unlock()
		}), nil
	}
}

func (owner *Coordinator) Load(requestContext context.Context, identifier session.SessionID) (Inspection, error) {
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
		release, err := owner.acquire(requestContext, identifier)
		if err != nil {
			return Inspection{}, err
		}
		loaded, stable, err := owner.loadCold(requestContext, identifier, true)
		release()
		if err != nil {
			return Inspection{}, err
		}
		if stable {
			return loaded, nil
		}
	}
}

func (owner *Coordinator) Inspect(requestContext context.Context, identifier session.SessionID) (Inspection, error) {
	if requestContext == nil {
		return Inspection{}, errors.New("session persistence: inspect Context is nil")
	}
	if conversation, found := owner.sessions.Get(identifier); found {
		return Inspection{Header: conversation.Header(), Events: conversation.Events()}, nil
	}
	release, err := owner.acquire(requestContext, identifier)
	if err != nil {
		return Inspection{}, err
	}
	defer release()
	stored, found, err := owner.storage.LoadStored(requestContext, identifier)
	if err != nil {
		return Inspection{}, err
	}
	if !found {
		return Inspection{}, &NotFoundError{ID: identifier}
	}
	loaded, err := owner.logicalInspection(identifier, stored)
	if err != nil {
		return Inspection{}, err
	}
	closers, err := interruptedTurnClosers(loaded.Events)
	if err != nil {
		return Inspection{}, owner.corruption(identifier, err)
	}
	loaded.Events = append(loaded.Events, closers...)
	if _, err := inspectStored(loaded.Header, loaded.Events); err != nil {
		return Inspection{}, owner.normalizeLoadError(identifier, loaded.Header, err)
	}
	return loaded, nil
}

func (owner *Coordinator) ReadFrom(requestContext context.Context, identifier session.SessionID, fromSeq int64) (Inspection, error) {
	if fromSeq < 0 {
		return Inspection{}, errors.New("session persistence: fromSeq must be non-negative")
	}
	release, err := owner.acquire(requestContext, identifier)
	if err != nil {
		return Inspection{}, err
	}
	defer release()
	stored, found, err := owner.storage.LoadStored(requestContext, identifier)
	if err != nil {
		return Inspection{}, err
	}
	if !found {
		return Inspection{}, &NotFoundError{ID: identifier}
	}
	loaded, err := owner.logicalInspection(identifier, stored)
	if err != nil {
		return Inspection{}, err
	}
	start := len(loaded.Events)
	for index, entry := range loaded.Events {
		if entry.Seq >= fromSeq {
			start = index
			break
		}
	}
	return Inspection{Header: loaded.Header, Events: snapshotEvents(loaded.Events[start:])}, nil
}

func (owner *Coordinator) List(requestContext context.Context) ([]session.Header, error) {
	return owner.storage.ListStored(requestContext)
}

func (owner *Coordinator) ListSnapshots(requestContext context.Context) ([]Snapshot, error) {
	return owner.storage.ListStoredSnapshots(requestContext)
}

func (owner *Coordinator) onCreated(requestContext context.Context, conversation *session.Session) error {
	if conversation == nil {
		return errors.New("session persistence: created Session is nil")
	}
	identifier := conversation.ID()
	release, err := owner.acquire(requestContext, identifier)
	if err != nil {
		return err
	}
	defer release()
	seed := conversation.Events()
	metadata := conversation.Header()

	owner.mutex.Lock()
	reservation := owner.reservations[identifier]
	tracked := owner.states[identifier]
	owner.mutex.Unlock()
	if reservation != nil {
		if reservation.conversation != conversation {
			return fmt.Errorf("session persistence: persisted state for %q belongs to another unpublished Session", identifier)
		}
		owner.mutex.Lock()
		delete(owner.reservations, identifier)
		if tracked == nil {
			tracked = &durableState{metadata: metadata, cursor: reservation.cursor, materialized: true}
			owner.states[identifier] = tracked
		}
		tracked.owner = conversation
		owner.mutex.Unlock()
		if err := owner.appendCore(requestContext, identifier, seed[reservation.cursor:]); err != nil {
			return err
		}
		owner.installLive(conversation)
		return nil
	}
	if tracked != nil {
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
		owner.mutex.Lock()
		owner.states[identifier] = tracked
		owner.mutex.Unlock()
		if err := owner.appendCore(requestContext, identifier, seed[tracked.cursor:]); err != nil {
			return err
		}
		owner.installLive(conversation)
		return nil
	}

	tracked = &durableState{metadata: metadata, owner: conversation}
	owner.mutex.Lock()
	owner.states[identifier] = tracked
	owner.mutex.Unlock()
	if err := owner.appendCore(requestContext, identifier, seed); err != nil {
		return err
	}
	owner.installLive(conversation)
	return nil
}

func (owner *Coordinator) onEvent(_ context.Context, conversation *session.Session, committed session.Event) error {
	owner.mutex.Lock()
	live := owner.live[conversation]
	owner.mutex.Unlock()
	if live == nil {
		return fmt.Errorf("session persistence: event for uninitialized session %q", conversation.ID())
	}
	live.writes.enqueue(committed)
	return nil
}

func (owner *Coordinator) onFlush(requestContext context.Context, conversation *session.Session) error {
	owner.mutex.Lock()
	live := owner.live[conversation]
	owner.mutex.Unlock()
	if live == nil {
		return fmt.Errorf("session persistence: flush for uninitialized session %q", conversation.ID())
	}
	return live.writes.flush(requestContext)
}

func (owner *Coordinator) onDisposed(requestContext context.Context, conversation *session.Session) error {
	owner.mutex.Lock()
	live := owner.live[conversation]
	owner.mutex.Unlock()
	if live == nil {
		return nil
	}
	if err := live.writes.flush(requestContext); err != nil {
		return err
	}
	release, err := owner.acquire(requestContext, conversation.ID())
	if err != nil {
		return err
	}
	owner.mutex.Lock()
	delete(owner.live, conversation)
	if tracked := owner.states[conversation.ID()]; tracked != nil && tracked.owner == conversation {
		delete(owner.states, conversation.ID())
	}
	owner.mutex.Unlock()
	release()
	return nil
}

func (owner *Coordinator) installLive(conversation *session.Session) {
	identifier := conversation.ID()
	writes := newLiveWriter(owner.delay, func(requestContext context.Context, entries []session.Event) error {
		release, err := owner.acquire(requestContext, identifier)
		if err != nil {
			return err
		}
		defer release()
		return owner.appendLiveBatch(requestContext, identifier, entries)
	}, func(problem error) {
		owner.safeReport(fmt.Errorf("%s: background write for session %q failed; events retained: %w", owner.storage.BackendName(), identifier, problem))
	})
	owner.mutex.Lock()
	owner.live[conversation] = &liveSessionState{conversation: conversation, writes: writes}
	owner.mutex.Unlock()
}

func (owner *Coordinator) appendLiveBatch(requestContext context.Context, identifier session.SessionID, entries []session.Event) error {
	owner.mutex.Lock()
	tracked := owner.states[identifier]
	owner.mutex.Unlock()
	if tracked == nil {
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

func (owner *Coordinator) appendCore(requestContext context.Context, identifier session.SessionID, entries []session.Event) error {
	if len(entries) == 0 {
		return nil
	}
	owner.mutex.Lock()
	if owner.reservations[identifier] != nil {
		owner.mutex.Unlock()
		return fmt.Errorf("session persistence: cannot append session %q while its preparation is reserved", identifier)
	}
	tracked := owner.states[identifier]
	owner.mutex.Unlock()
	for tracked == nil {
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
		owner.mutex.Lock()
		tracked = owner.states[identifier]
		if tracked == nil {
			tracked = &durableState{metadata: loaded.Header, cursor: int64(len(loaded.Events)), materialized: true}
			owner.states[identifier] = tracked
		}
		owner.mutex.Unlock()
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
	return nil
}

func (owner *Coordinator) loadCold(requestContext context.Context, identifier session.SessionID, commit bool) (Inspection, bool, error) {
	stored, found, err := owner.storage.LoadStored(requestContext, identifier)
	if err != nil {
		return Inspection{}, false, err
	}
	if !found {
		return Inspection{}, false, &NotFoundError{ID: identifier}
	}
	if !commit {
		loaded, err := owner.logicalInspection(identifier, stored)
		return loaded, true, err
	}
	return owner.commitCold(requestContext, identifier, stored)
}

func (owner *Coordinator) commitCold(
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
	owner.mutex.Lock()
	tracked := owner.states[identifier]
	if tracked == nil || tracked.owner == nil {
		owner.states[identifier] = &durableState{
			metadata: loaded.Header, cursor: int64(len(loaded.Events)), materialized: true,
		}
	}
	owner.mutex.Unlock()
	return loaded, true, nil
}

func (owner *Coordinator) logicalInspection(identifier session.SessionID, stored StoredPrefix) (Inspection, error) {
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

func (owner *Coordinator) normalizeLoadError(identifier session.SessionID, metadata session.Header, problem error) error {
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

func (owner *Coordinator) corruption(identifier session.SessionID, problem error) error {
	var storedFailure *CorruptionError
	if errors.As(problem, &storedFailure) {
		return problem
	}
	return &CorruptionError{ID: identifier, Cause: problem}
}

func (owner *Coordinator) acquire(requestContext context.Context, identifier session.SessionID) (func(), error) {
	if requestContext == nil {
		return nil, errors.New("session persistence: Context is nil")
	}
	owner.mutex.Lock()
	if owner.closed {
		owner.mutex.Unlock()
		return nil, errors.New("session persistence: coordinator is closed")
	}
	gate := owner.gates[identifier]
	if gate == nil {
		gate = &serialGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		owner.gates[identifier] = gate
	}
	gate.refs++
	owner.mutex.Unlock()
	select {
	case <-gate.token:
		return func() {
			gate.token <- struct{}{}
			owner.mutex.Lock()
			gate.refs--
			if gate.refs == 0 && owner.gates[identifier] == gate {
				delete(owner.gates, identifier)
			}
			owner.mutex.Unlock()
		}, nil
	case <-requestContext.Done():
		owner.mutex.Lock()
		gate.refs--
		if gate.refs == 0 && owner.gates[identifier] == gate {
			delete(owner.gates, identifier)
		}
		owner.mutex.Unlock()
		return nil, context.Cause(requestContext)
	}
}

func (owner *Coordinator) close(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	owner.mutex.Lock()
	if owner.closed {
		closeErr := owner.closeErr
		owner.mutex.Unlock()
		return closeErr
	}
	if owner.closing {
		done := owner.closeDone
		owner.mutex.Unlock()
		select {
		case <-done:
			owner.mutex.Lock()
			closeErr := owner.closeErr
			owner.mutex.Unlock()
			return closeErr
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	}
	owner.closing = true
	liveStates := make([]*liveSessionState, 0, len(owner.live))
	for _, live := range owner.live {
		liveStates = append(liveStates, live)
	}
	owner.mutex.Unlock()
	var closeErr error
	for _, live := range liveStates {
		closeErr = errors.Join(closeErr, live.writes.flush(closeContext))
	}
	closeErr = errors.Join(closeErr, owner.storage.Close(closeContext))
	owner.mutex.Lock()
	owner.closeErr = closeErr
	owner.closed = true
	owner.closing = false
	close(owner.closeDone)
	owner.mutex.Unlock()
	return closeErr
}

func (owner *Coordinator) safeReport(problem error) {
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
