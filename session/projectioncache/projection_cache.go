package projectioncache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

// ErrClosed reports that Projection Cache is not open for new operations.
var ErrClosed = errors.New("session projection cache is closed")

// cacheState is the exclusive lifecycle phase of ProjectionCache.
type cacheState uint8

const (
	// cacheCreated has configuration but no installed runtime capabilities.
	cacheCreated cacheState = iota
	// cacheOpening loads durable records before publishing runtime capabilities.
	cacheOpening
	// cacheOpen accepts read and live checkpoint operations.
	cacheOpen
	// cacheClosing rejects new work while entered operations drain.
	cacheClosing
	// cacheClosed has completed the shared close attempt.
	cacheClosed
)

// ProjectionCache is the lifecycle facade for checkpoint reads and live
// Session inputs. Scheduling, restore, and record state belong to its focused
// collaborators.
type ProjectionCache struct {
	mutex sync.Mutex
	// state controls runtime capability admission and shutdown.
	state cacheState

	// writeEveryEvents is the count trigger for a live checkpoint.
	writeEveryEvents int
	// writeInterval is the time trigger for a dirty live checkpoint.
	writeInterval time.Duration
	// failures receives contained checkpoint and write-back failures.
	failures FailureReporter
	// live owns exact-Session checkpoint scheduling after Open.
	live *liveCheckpointer
	// reader owns cached and cold snapshot reconstruction after Open.
	reader *snapshotReader
	// records owns reusable checkpoint records and the durable Store after Open.
	records *checkpointRecords
	// inflight counts facade calls admitted before Close.
	inflight sync.WaitGroup
	// openDone closes when an in-progress Open either publishes or rolls back.
	openDone chan struct{}
	// closeDone closes after the shared close attempt completes.
	closeDone chan struct{}
	// closeErr is the shared close result read after closeDone.
	closeErr error
}

// New constructs an inactive Projection Cache.
func New(settings Config) (*ProjectionCache, error) {
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
	return &ProjectionCache{
		state:            cacheCreated,
		writeEveryEvents: writeEveryEvents,
		writeInterval:    writeInterval,
		failures:         settings.Failures,
		closeDone:        make(chan struct{}),
	}, nil
}

// Open installs required capabilities and loads the durable checkpoint index.
func (owner *ProjectionCache) Open(
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
		return errors.New(
			"session projection cache: Open requires Sessions, Persistence, Projections, and Store",
		)
	}
	owner.mutex.Lock()
	if owner.state != cacheCreated {
		owner.mutex.Unlock()
		return errors.New("session projection cache: Open may only be called once")
	}
	owner.state = cacheOpening
	owner.openDone = make(chan struct{})
	owner.mutex.Unlock()
	records, err := openCheckpointRecords(requestContext, store, owner.failures)
	if err != nil {
		owner.mutex.Lock()
		owner.state = cacheCreated
		close(owner.openDone)
		owner.openDone = nil
		owner.mutex.Unlock()
		return fmt.Errorf("session projection cache: load checkpoints: %w", err)
	}
	live := newLiveCheckpointer(
		owner.writeEveryEvents,
		owner.writeInterval,
		sessions,
		projections,
		records,
		owner.failures,
	)
	reader := newSnapshotReader(persistence, projections, records, owner.failures)
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	owner.records = records
	owner.live = live
	owner.reader = reader
	owner.state = cacheOpen
	close(owner.openDone)
	owner.openDone = nil
	return nil
}

// EventAppended records one committed live Session event and schedules
// checkpoint work without performing storage I/O on the observer call.
func (owner *ProjectionCache) EventAppended(
	conversation session.Context,
	committed session.Event,
) error {
	leave, err := owner.enter()
	if errors.Is(err, ErrClosed) {
		return nil
	}
	if err != nil {
		return err
	}
	defer leave()
	return owner.live.EventAppended(conversation, committed)
}

// SessionDisposed requests the one detached final checkpoint for an exact
// Session, including a Session that produced no observed append event.
func (owner *ProjectionCache) SessionDisposed(conversation session.Context) error {
	leave, err := owner.enter()
	if errors.Is(err, ErrClosed) {
		return nil
	}
	if err != nil {
		return err
	}
	defer leave()
	return owner.live.SessionDisposed(conversation)
}

func (owner *ProjectionCache) CachedSnapshot(
	metadata session.Header,
) (*sessionprojection.Snapshot, error) {
	leave, err := owner.enter()
	if err != nil {
		return nil, err
	}
	defer leave()
	return owner.reader.CachedSnapshot(metadata)
}

func (owner *ProjectionCache) ColdSnapshot(
	requestContext context.Context,
	identifier session.SessionID,
) (sessionprojection.Snapshot, error) {
	leave, err := owner.enter()
	if err != nil {
		return sessionprojection.Snapshot{}, err
	}
	defer leave()
	return owner.reader.ColdSnapshot(requestContext, identifier)
}

// Close rejects new work, stops timers, drains entered checkpoint work, and
// closes the checkpoint Store after every record replacement has finished.
func (owner *ProjectionCache) Close(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	for {
		owner.mutex.Lock()
		if owner.state != cacheOpening {
			break
		}
		opened := owner.openDone
		owner.mutex.Unlock()
		select {
		case <-opened:
			continue
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	}
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
	}
	owner.mutex.Unlock()

	owner.inflight.Wait()
	owner.live.Close()
	closeErr := owner.records.Close(context.WithoutCancel(closeContext))
	owner.mutex.Lock()
	owner.state = cacheClosed
	owner.closeErr = closeErr
	close(owner.closeDone)
	owner.mutex.Unlock()
	return closeErr
}

func (owner *ProjectionCache) enter() (func(), error) {
	owner.mutex.Lock()
	if owner.state != cacheOpen {
		owner.mutex.Unlock()
		return nil, ErrClosed
	}
	owner.inflight.Add(1)
	owner.mutex.Unlock()
	return owner.inflight.Done, nil
}

func reportFailure(reporter FailureReporter, reported Failure) {
	defer func() { _ = recover() }()
	reporter.ReportProjectionCacheFailure(reported)
}

var _ Cache = (*ProjectionCache)(nil)
