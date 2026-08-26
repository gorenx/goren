// Package projectioncache owns rebuildable Session projection checkpoints.
// Durable Session events remain the only source of truth.
package projectioncache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sessionpersistence "github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

const (
	PluginName              = "@deepseek-ai/dsh-session-projection-cache"
	DefaultWriteEveryEvents = 200
	DefaultWriteInterval    = 5 * time.Second
)

var ErrClosed = errors.New("session projection cache is closed")

// Cache offers read-only checkpoint reuse to Session read use cases.
type Cache interface {
	plugin.Service
	CachedSnapshot(session.Header) (*sessionprojection.Snapshot, error)
	ColdSnapshot(context.Context, session.SessionID) (sessionprojection.Snapshot, error)
}

// CheckpointStore persists one replaceable checkpoint record per Session.
type CheckpointStore interface {
	LoadAll(context.Context) (map[session.SessionID]CheckpointRecord, error)
	Replace(context.Context, session.SessionID, CheckpointRecord) error
	Close(context.Context) error
}

// LogIdentity prevents a reused Session ID from inheriting another Session
// lifecycle's rebuildable state.
type LogIdentity struct {
	CreatedAt int64   `json:"createdAt"`
	CWD       *string `json:"cwd,omitempty"`
}

// CheckpointRecord is one Session's current complete set of Unit rows.
type CheckpointRecord struct {
	Identity LogIdentity                  `json:"identity"`
	Rows     sessionprojection.Checkpoint `json:"rows"`
}

// Failure identifies contained cache work that cannot change Session truth.
type Failure struct {
	SessionID session.SessionID
	Operation string
	Error     error
}

// FailureReporter receives contained checkpoint write and write-back failures.
type FailureReporter interface {
	ReportProjectionCacheFailure(Failure)
}

// Config controls checkpoint scheduling, not physical storage.
type Config struct {
	WriteEveryEvents int
	WriteInterval    time.Duration
	Failures         FailureReporter
}

type cacheState uint8

const (
	cacheCreated cacheState = iota
	cacheOpen
	cacheClosing
	cacheClosed
)

type writeState struct {
	conversation session.Context
	latestSeq    int64
	persistedSeq int64
	generation   uint64
	persistedGen uint64
	pending      int
	timer        *time.Timer
	writing      bool
	requested    bool
	force        bool
	retiring     bool
}

// CheckpointCache owns the in-memory record index, cold restoration, and live
// checkpoint scheduling. Plugin lifecycle adaptation lives in a separate package.
type CheckpointCache struct {
	mutex            sync.Mutex
	state            cacheState
	writeEveryEvents int
	writeInterval    time.Duration
	failures         FailureReporter
	sessions         session.LiveStore
	persistence      sessionpersistence.Persistence
	projections      sessionprojection.Registry
	store            CheckpointStore
	records          map[session.SessionID]CheckpointRecord
	writes           map[session.Context]*writeState
	recordLocks      map[session.SessionID]*sync.Mutex
	inflight         sync.WaitGroup
	closeDone        chan struct{}
	closeErr         error
}

// New constructs an inactive checkpoint cache.
func New(settings Config) (*CheckpointCache, error) {
	writeEveryEvents := settings.WriteEveryEvents
	if writeEveryEvents == 0 {
		writeEveryEvents = DefaultWriteEveryEvents
	}
	if writeEveryEvents < 1 {
		return nil, errors.New("session projection cache: writeEveryEvents must be positive")
	}
	writeInterval := settings.WriteInterval
	if writeInterval == 0 {
		writeInterval = DefaultWriteInterval
	}
	if writeInterval < time.Millisecond {
		return nil, errors.New("session projection cache: writeInterval must be at least one millisecond")
	}
	if settings.Failures == nil {
		return nil, errors.New("session projection cache: failure reporter is required")
	}
	return &CheckpointCache{
		state:            cacheCreated,
		writeEveryEvents: writeEveryEvents,
		writeInterval:    writeInterval,
		failures:         settings.Failures,
		records:          make(map[session.SessionID]CheckpointRecord),
		writes:           make(map[session.Context]*writeState),
		recordLocks:      make(map[session.SessionID]*sync.Mutex),
		closeDone:        make(chan struct{}),
	}, nil
}

// Open installs required capabilities and loads the durable checkpoint index.
func (owner *CheckpointCache) Open(
	requestContext context.Context,
	sessions session.LiveStore,
	persistence sessionpersistence.Persistence,
	projections sessionprojection.Registry,
	store CheckpointStore,
) error {
	if requestContext == nil {
		return errors.New("session projection cache: Open Context is nil")
	}
	if err := requestContext.Err(); err != nil {
		return err
	}
	if sessions == nil || persistence == nil || projections == nil || store == nil {
		return errors.New("session projection cache: Open requires Sessions, Persistence, Projections, and Store")
	}
	loaded, err := store.LoadAll(requestContext)
	if err != nil {
		return fmt.Errorf("session projection cache: load checkpoints: %w", err)
	}
	validated := make(map[session.SessionID]CheckpointRecord, len(loaded))
	for identifier, record := range loaded {
		detached, validationErr := ValidateCheckpointRecord(identifier, record)
		if validationErr != nil {
			owner.report(Failure{
				SessionID: identifier,
				Operation: "load checkpoint",
				Error:     validationErr,
			})
			continue
		}
		validated[identifier] = detached
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.state != cacheCreated {
		return errors.New("session projection cache: Open may only be called once")
	}
	owner.sessions = sessions
	owner.persistence = persistence
	owner.projections = projections
	owner.store = store
	owner.records = validated
	owner.state = cacheOpen
	return nil
}

// Close rejects new work, stops timers, drains entered storage operations, and
// then closes the checkpoint store.
func (owner *CheckpointCache) Close(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	owner.mutex.Lock()
	switch owner.state {
	case cacheClosed:
		result := owner.closeErr
		owner.mutex.Unlock()
		return result
	case cacheClosing:
		done := owner.closeDone
		owner.mutex.Unlock()
		select {
		case <-done:
			owner.mutex.Lock()
			result := owner.closeErr
			owner.mutex.Unlock()
			return result
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	case cacheCreated:
		owner.state = cacheClosed
		close(owner.closeDone)
		owner.mutex.Unlock()
		return nil
	case cacheOpen:
		owner.state = cacheClosing
		for conversation, state := range owner.writes {
			if state.timer != nil {
				state.timer.Stop()
				state.timer = nil
			}
			if !state.writing {
				delete(owner.writes, conversation)
			}
		}
	}
	store := owner.store
	owner.mutex.Unlock()

	owner.inflight.Wait()
	closeErr := store.Close(context.WithoutCancel(closeContext))
	owner.mutex.Lock()
	owner.records = make(map[session.SessionID]CheckpointRecord)
	owner.writes = make(map[session.Context]*writeState)
	owner.state = cacheClosed
	owner.closeErr = closeErr
	close(owner.closeDone)
	owner.mutex.Unlock()
	return closeErr
}

func (owner *CheckpointCache) beginOperation() error {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.state != cacheOpen {
		return ErrClosed
	}
	owner.inflight.Add(1)
	return nil
}

func (owner *CheckpointCache) recordLock(identifier session.SessionID) *sync.Mutex {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	selected := owner.recordLocks[identifier]
	if selected == nil {
		selected = &sync.Mutex{}
		owner.recordLocks[identifier] = selected
	}
	return selected
}

func identityOf(metadata session.Header) LogIdentity {
	return LogIdentity{
		CreatedAt: metadata.CreatedAt,
		CWD:       cloneString(metadata.CWD),
	}
}

func sameIdentity(left LogIdentity, right LogIdentity) bool {
	if left.CreatedAt != right.CreatedAt {
		return false
	}
	if left.CWD == nil || right.CWD == nil {
		return left.CWD == nil && right.CWD == nil
	}
	return *left.CWD == *right.CWD
}

// ValidateCheckpointRecord validates and detaches one owner-defined persisted
// record before it crosses a storage or in-memory index boundary.
func ValidateCheckpointRecord(
	identifier session.SessionID,
	record CheckpointRecord,
) (CheckpointRecord, error) {
	if identifier == "" {
		return CheckpointRecord{}, errors.New("checkpoint Session ID is empty")
	}
	if record.Identity.CreatedAt < 0 {
		return CheckpointRecord{}, errors.New("checkpoint createdAt is negative")
	}
	detached := CheckpointRecord{
		Identity: LogIdentity{
			CreatedAt: record.Identity.CreatedAt,
			CWD:       cloneString(record.Identity.CWD),
		},
		Rows: make(sessionprojection.Checkpoint, len(record.Rows)),
	}
	for projectionKey, row := range record.Rows {
		if projectionKey == "" {
			return CheckpointRecord{}, errors.New("checkpoint projection key is empty")
		}
		if row.Version < 0 {
			return CheckpointRecord{}, fmt.Errorf("checkpoint %q version is negative", projectionKey)
		}
		if row.Seq < -1 {
			return CheckpointRecord{}, fmt.Errorf("checkpoint %q seq is below -1", projectionKey)
		}
		value, err := jsonvalue.Clone(row.Value)
		if err != nil {
			return CheckpointRecord{}, fmt.Errorf(
				"checkpoint %q value is not valid plain JSON: %w",
				projectionKey,
				err,
			)
		}
		detached.Rows[projectionKey] = sessionprojection.CheckpointRow{
			Version: row.Version,
			Seq:     row.Seq,
			Value:   value,
		}
	}
	return detached, nil
}

func cloneRecord(record CheckpointRecord) CheckpointRecord {
	detached, _ := ValidateCheckpointRecord("detached", record)
	return detached
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}

func cloneRaw(source json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), source...)
}

func (owner *CheckpointCache) report(reported Failure) {
	defer func() { _ = recover() }()
	owner.failures.ReportProjectionCacheFailure(reported)
}

var _ Cache = (*CheckpointCache)(nil)
