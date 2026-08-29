package projectioncache

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

// liveCheckpointerState identifies whether new live Session work is accepted.
type liveCheckpointerState uint8

const (
	// liveCheckpointerAccepting admits events and disposal notifications.
	liveCheckpointerAccepting liveCheckpointerState = iota
	// liveCheckpointerStopping rejects new work while existing attempts drain.
	liveCheckpointerStopping
)

// liveCheckpointer owns live checkpoint policy, exact-Session scheduling, and
// checkpoint attempt execution. It does not own Cache admission or records.
type liveCheckpointer struct {
	mutex sync.Mutex

	// state controls admission while Close stops timers and drains attempts.
	state liveCheckpointerState
	// writes maps each exact live Session key to its scheduling state value.
	writes map[session.Context]*liveWrite

	writeEveryEvents int
	writeInterval    time.Duration
	sessions         LiveSessionFlusher
	projections      CheckpointProjector
	records          *checkpointRecords
	failures         FailureReporter
	// inflight counts checkpoint goroutines started before shutdown admission closes.
	inflight sync.WaitGroup
}

func newLiveCheckpointer(
	writeEveryEvents int,
	writeInterval time.Duration,
	sessions LiveSessionFlusher,
	projections CheckpointProjector,
	records *checkpointRecords,
	failures FailureReporter,
) *liveCheckpointer {
	return &liveCheckpointer{
		writes:           make(map[session.Context]*liveWrite),
		writeEveryEvents: writeEveryEvents,
		writeInterval:    writeInterval,
		sessions:         sessions,
		projections:      projections,
		records:          records,
		failures:         failures,
	}
}

func (checkpointer *liveCheckpointer) EventAppended(
	conversation session.Context,
	committed session.Event,
) error {
	if conversation == nil {
		return errors.New("session projection cache: EventAppended Session is nil")
	}
	write := checkpointer.writeFor(conversation)
	if write == nil {
		return nil
	}
	shouldStart, timer := write.observe(
		committed.Type == session.TurnEndEventName,
		checkpointer.writeEveryEvents,
	)
	checkpointer.scheduleTimer(conversation, write, timer)
	if shouldStart {
		checkpointer.start(conversation, write)
	}
	return nil
}

func (checkpointer *liveCheckpointer) SessionDisposed(
	conversation session.Context,
) error {
	if conversation == nil {
		return errors.New("session projection cache: SessionDisposed Session is nil")
	}
	write := checkpointer.writeFor(conversation)
	if write == nil {
		return nil
	}
	if write.detach() {
		checkpointer.start(conversation, write)
	}
	return nil
}

func (checkpointer *liveCheckpointer) Close() {
	checkpointer.mutex.Lock()
	if checkpointer.state == liveCheckpointerStopping {
		checkpointer.mutex.Unlock()
		checkpointer.inflight.Wait()
		return
	}
	checkpointer.state = liveCheckpointerStopping
	writes := make(map[session.Context]*liveWrite, len(checkpointer.writes))
	for conversation, write := range checkpointer.writes {
		writes[conversation] = write
	}
	checkpointer.mutex.Unlock()
	for conversation, write := range writes {
		if write.stop() {
			checkpointer.remove(conversation, write)
		}
	}
	checkpointer.inflight.Wait()
	checkpointer.mutex.Lock()
	checkpointer.writes = make(map[session.Context]*liveWrite)
	checkpointer.mutex.Unlock()
}

func (checkpointer *liveCheckpointer) writeFor(
	conversation session.Context,
) *liveWrite {
	checkpointer.mutex.Lock()
	defer checkpointer.mutex.Unlock()
	if checkpointer.state == liveCheckpointerStopping {
		return nil
	}
	write := checkpointer.writes[conversation]
	if write == nil {
		write = &liveWrite{}
		checkpointer.writes[conversation] = write
	}
	return write
}

func (checkpointer *liveCheckpointer) scheduleTimer(
	conversation session.Context,
	write *liveWrite,
	request timerRequest,
) {
	if request.generation == 0 {
		return
	}
	timer := time.AfterFunc(checkpointer.writeInterval, func() {
		checkpointer.timerElapsed(conversation, write, request.generation)
	})
	write.installTimer(request, timer)
}

func (checkpointer *liveCheckpointer) timerElapsed(
	conversation session.Context,
	write *liveWrite,
	generation uint64,
) {
	checkpointer.mutex.Lock()
	active := checkpointer.state == liveCheckpointerAccepting &&
		checkpointer.writes[conversation] == write
	checkpointer.mutex.Unlock()
	if !active || !write.timerElapsed(generation) {
		return
	}
	checkpointer.start(conversation, write)
}

func (checkpointer *liveCheckpointer) start(
	conversation session.Context,
	write *liveWrite,
) {
	checkpointer.mutex.Lock()
	if checkpointer.state == liveCheckpointerStopping ||
		checkpointer.writes[conversation] != write {
		checkpointer.mutex.Unlock()
		return
	}
	checkpointer.inflight.Add(1)
	checkpointer.mutex.Unlock()
	go checkpointer.run(conversation, write)
}

func (checkpointer *liveCheckpointer) run(
	conversation session.Context,
	write *liveWrite,
) {
	defer checkpointer.inflight.Done()
	for {
		attempt := write.beginAttempt()
		err := checkpointer.persistCheckpoint(conversation, attempt)
		detached := attempt.kind == liveCheckpoint &&
			errors.Is(err, session.ErrNotAttached)
		if err != nil && !detached {
			reportFailure(
				checkpointer.failures,
				Failure{
					SessionID: conversation.ID(),
					Operation: "live checkpoint",
					Error:     err,
				},
			)
		}

		switch write.finishAttempt(attempt, err == nil) {
		case liveWriteContinue:
			continue
		case liveWriteRetry:
			checkpointer.scheduleTimer(conversation, write, write.retryTimer())
			return
		case liveWriteIdle:
			return
		case liveWriteRemove:
			checkpointer.remove(conversation, write)
			return
		}
	}
}

func (checkpointer *liveCheckpointer) remove(
	conversation session.Context,
	write *liveWrite,
) {
	checkpointer.mutex.Lock()
	if checkpointer.writes[conversation] == write {
		delete(checkpointer.writes, conversation)
	}
	checkpointer.mutex.Unlock()
}

func (checkpointer *liveCheckpointer) persistCheckpoint(
	conversation session.Context,
	attempt checkpointAttempt,
) error {
	rows, err := checkpointer.projections.Checkpoint(conversation)
	if err != nil {
		return err
	}
	if err := validateCheckpointCut(rows); err != nil {
		return err
	}
	metadata := conversation.Header()
	if attempt.kind == liveCheckpoint {
		if err := checkpointer.sessions.Flush(context.Background(), conversation); err != nil {
			return err
		}
	}
	return checkpointer.records.Replace(
		context.Background(),
		conversation.ID(),
		CheckpointRecord{
			Identity: identityOf(metadata),
			Rows:     rows,
		},
	)
}

func validateCheckpointCut(rows sessionprojection.Checkpoint) error {
	var cut int64
	found := false
	for _, row := range rows {
		if !found {
			cut = row.Seq
			found = true
			continue
		}
		if row.Seq != cut {
			return errors.New(
				"session projection cache: checkpoint rows do not share one event cut",
			)
		}
	}
	return nil
}
