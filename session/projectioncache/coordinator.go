package projectioncache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorenx/goren/session"
)

var ErrClosed = errors.New("session projection cache is closed")

type cacheState uint8

const (
	cacheCreated cacheState = iota
	cacheOpen
	cacheClosing
	cacheClosed
)

// Coordinator owns Projection Cache admission, dependencies, and the registry
// of per-Session caches. Record and writer state belong to sessionCache.
type Coordinator struct {
	mutex            sync.Mutex
	state            cacheState
	writeEveryEvents int
	writeInterval    time.Duration
	failures         FailureReporter
	sessions         LiveSessionFlusher
	persistence      DurableEventReader
	projections      CheckpointProjector
	store            CheckpointStore
	checkpoints      map[session.SessionID]*sessionCache
	inflight         sync.WaitGroup
	closeDone        chan struct{}
	closeErr         error
}

// New constructs an inactive Projection Cache coordinator.
func New(settings Config) (*Coordinator, error) {
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
	return &Coordinator{
		state:            cacheCreated,
		writeEveryEvents: writeEveryEvents,
		writeInterval:    writeInterval,
		failures:         settings.Failures,
		checkpoints:      make(map[session.SessionID]*sessionCache),
		closeDone:        make(chan struct{}),
	}, nil
}

// Open installs required capabilities and loads the durable checkpoint index.
func (owner *Coordinator) Open(
	requestContext context.Context,
	sessions LiveSessionFlusher,
	persistence DurableEventReader,
	projections CheckpointProjector,
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
	owner.checkpoints = make(map[session.SessionID]*sessionCache, len(validated))
	for identifier, record := range validated {
		owner.checkpoints[identifier] = newSessionCache(owner, identifier, record, true)
	}
	owner.state = cacheOpen
	return nil
}

// Close rejects new work, stops timers, drains entered storage operations, and
// then closes the checkpoint store.
func (owner *Coordinator) Close(closeContext context.Context) error {
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
		checkpoints := make([]*sessionCache, 0, len(owner.checkpoints))
		for _, checkpoint := range owner.checkpoints {
			checkpoints = append(checkpoints, checkpoint)
		}
		owner.mutex.Unlock()
		for _, checkpoint := range checkpoints {
			checkpoint.stop()
		}
		owner.mutex.Lock()
	}
	store := owner.store
	owner.mutex.Unlock()

	owner.inflight.Wait()
	closeErr := store.Close(context.WithoutCancel(closeContext))
	owner.mutex.Lock()
	owner.checkpoints = make(map[session.SessionID]*sessionCache)
	owner.state = cacheClosed
	owner.closeErr = closeErr
	close(owner.closeDone)
	owner.mutex.Unlock()
	return closeErr
}

func (owner *Coordinator) enterSession(
	identifier session.SessionID,
) (*sessionCache, func(), error) {
	owner.mutex.Lock()
	if owner.state != cacheOpen {
		owner.mutex.Unlock()
		return nil, nil, ErrClosed
	}
	checkpoint := owner.checkpoints[identifier]
	if checkpoint == nil {
		checkpoint = newSessionCache(owner, identifier, CheckpointRecord{}, false)
		owner.checkpoints[identifier] = checkpoint
	}
	owner.inflight.Add(1)
	owner.mutex.Unlock()
	return checkpoint, owner.inflight.Done, nil
}

func (owner *Coordinator) startAsync(operation func()) bool {
	owner.mutex.Lock()
	if owner.state != cacheOpen {
		owner.mutex.Unlock()
		return false
	}
	owner.inflight.Add(1)
	owner.mutex.Unlock()
	go func() {
		defer owner.inflight.Done()
		operation()
	}()
	return true
}

func (owner *Coordinator) isOpen() bool {
	owner.mutex.Lock()
	accepting := owner.state == cacheOpen
	owner.mutex.Unlock()
	return accepting
}

func (owner *Coordinator) report(reported Failure) {
	defer func() { _ = recover() }()
	owner.failures.ReportProjectionCacheFailure(reported)
}

var _ Cache = (*Coordinator)(nil)
