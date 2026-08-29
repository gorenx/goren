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
	// lifecycles maps each exact live Session key to its checkpoint lifecycle value.
	lifecycles map[session.Context]*checkpointLifecycle

	writeEveryEvents int
	writeInterval    time.Duration
	sessions         LiveSessionFlusher
	projections      CheckpointProjector
	records          *checkpointRecords
	failures         FailureReporter
	// inflight counts checkpoint goroutines started before shutdown admission closes.
	inflight sync.WaitGroup
}

// checkpointResult is the state-machine outcome and optional reportable error
// produced from one port execution.
type checkpointResult struct {
	outcome checkpointOutcome
	failure error
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
		lifecycles:       make(map[session.Context]*checkpointLifecycle),
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
	lifecycle := checkpointer.lifecycleFor(conversation)
	if lifecycle == nil {
		return nil
	}
	command, err := lifecycle.eventAppended(
		committed.Type == session.TurnEndEventName,
		checkpointer.writeEveryEvents,
	)
	if err != nil {
		return err
	}
	return checkpointer.execute(conversation, lifecycle, command)
}

func (checkpointer *liveCheckpointer) SessionDisposed(
	conversation session.Context,
) error {
	if conversation == nil {
		return errors.New("session projection cache: SessionDisposed Session is nil")
	}
	lifecycle := checkpointer.lifecycleFor(conversation)
	if lifecycle == nil {
		return nil
	}
	command, err := lifecycle.sessionDisposed()
	if err != nil {
		return err
	}
	return checkpointer.execute(conversation, lifecycle, command)
}

func (checkpointer *liveCheckpointer) Close() {
	checkpointer.mutex.Lock()
	if checkpointer.state == liveCheckpointerStopping {
		checkpointer.mutex.Unlock()
		checkpointer.inflight.Wait()
		return
	}
	checkpointer.state = liveCheckpointerStopping
	lifecycles := make(map[session.Context]*checkpointLifecycle, len(checkpointer.lifecycles))
	for conversation, lifecycle := range checkpointer.lifecycles {
		lifecycles[conversation] = lifecycle
	}
	checkpointer.mutex.Unlock()
	for conversation, lifecycle := range lifecycles {
		command, err := lifecycle.cacheClosing()
		if err == nil {
			err = checkpointer.execute(conversation, lifecycle, command)
		}
		if err != nil {
			checkpointer.reportTransitionFailure(conversation, err)
		}
	}
	checkpointer.inflight.Wait()
	checkpointer.mutex.Lock()
	checkpointer.lifecycles = make(map[session.Context]*checkpointLifecycle)
	checkpointer.mutex.Unlock()
}

func (checkpointer *liveCheckpointer) lifecycleFor(
	conversation session.Context,
) *checkpointLifecycle {
	checkpointer.mutex.Lock()
	defer checkpointer.mutex.Unlock()
	if checkpointer.state == liveCheckpointerStopping {
		return nil
	}
	lifecycle := checkpointer.lifecycles[conversation]
	if lifecycle == nil {
		lifecycle = &checkpointLifecycle{}
		checkpointer.lifecycles[conversation] = lifecycle
	}
	return lifecycle
}

func (checkpointer *liveCheckpointer) scheduleTimer(
	conversation session.Context,
	lifecycle *checkpointLifecycle,
	generation uint64,
) {
	if generation == 0 {
		return
	}
	timer := time.AfterFunc(checkpointer.writeInterval, func() {
		checkpointer.timerElapsed(conversation, lifecycle, generation)
	})
	lifecycle.installTimer(generation, timer)
}

func (checkpointer *liveCheckpointer) timerElapsed(
	conversation session.Context,
	lifecycle *checkpointLifecycle,
	generation uint64,
) {
	checkpointer.mutex.Lock()
	active := checkpointer.state == liveCheckpointerAccepting &&
		checkpointer.lifecycles[conversation] == lifecycle
	checkpointer.mutex.Unlock()
	if !active {
		return
	}
	command, err := lifecycle.timerElapsed(generation)
	if err == nil {
		err = checkpointer.execute(conversation, lifecycle, command)
	}
	if err != nil {
		checkpointer.reportTransitionFailure(conversation, err)
	}
}

func (checkpointer *liveCheckpointer) start(
	conversation session.Context,
	lifecycle *checkpointLifecycle,
	attempt checkpointAttempt,
) {
	checkpointer.mutex.Lock()
	if checkpointer.state == liveCheckpointerStopping ||
		checkpointer.lifecycles[conversation] != lifecycle {
		checkpointer.mutex.Unlock()
		return
	}
	checkpointer.inflight.Add(1)
	checkpointer.mutex.Unlock()
	go checkpointer.run(conversation, lifecycle, attempt)
}

func (checkpointer *liveCheckpointer) run(
	conversation session.Context,
	lifecycle *checkpointLifecycle,
	attempt checkpointAttempt,
) {
	defer checkpointer.inflight.Done()
	for {
		err := checkpointer.persistCheckpoint(conversation, attempt)
		result := classifyCheckpointResult(attempt, err)
		if result.failure != nil {
			reportFailure(
				checkpointer.failures,
				Failure{
					SessionID: conversation.ID(),
					Operation: "live checkpoint",
					Error:     result.failure,
				},
			)
		}
		command, transitionErr := lifecycle.checkpointCompleted(
			attempt,
			result.outcome,
		)
		if transitionErr != nil {
			checkpointer.reportTransitionFailure(conversation, transitionErr)
			checkpointer.remove(conversation, lifecycle)
			return
		}
		if command.kind == checkpointLifecycleStartCheckpoint {
			attempt = command.attempt
			continue
		}
		if commandErr := checkpointer.execute(
			conversation,
			lifecycle,
			command,
		); commandErr != nil {
			checkpointer.reportTransitionFailure(conversation, commandErr)
			checkpointer.remove(conversation, lifecycle)
		}
		return
	}
}

func (checkpointer *liveCheckpointer) execute(
	conversation session.Context,
	lifecycle *checkpointLifecycle,
	command checkpointLifecycleCommand,
) error {
	switch command.kind {
	case checkpointLifecycleNoop:
		return nil
	case checkpointLifecycleArmTimer:
		if command.timerGeneration == 0 {
			return errors.New(
				"session projection cache: ArmTimer command has no generation",
			)
		}
		checkpointer.scheduleTimer(
			conversation,
			lifecycle,
			command.timerGeneration,
		)
		return nil
	case checkpointLifecycleStartCheckpoint:
		if command.attempt.id == 0 {
			return errors.New(
				"session projection cache: StartCheckpoint command has no attempt",
			)
		}
		checkpointer.start(conversation, lifecycle, command.attempt)
		return nil
	case checkpointLifecycleRemove:
		checkpointer.remove(conversation, lifecycle)
		return nil
	default:
		return errors.New("session projection cache: unknown checkpoint lifecycle command")
	}
}

func (checkpointer *liveCheckpointer) reportTransitionFailure(
	conversation session.Context,
	err error,
) {
	reportFailure(
		checkpointer.failures,
		Failure{
			SessionID: conversation.ID(),
			Operation: "live checkpoint state",
			Error:     err,
		},
	)
}

func classifyCheckpointResult(
	attempt checkpointAttempt,
	err error,
) checkpointResult {
	if err == nil {
		return checkpointResult{
			outcome: checkpointSucceeded,
		}
	}
	if attempt.kind == liveCheckpoint && errors.Is(err, session.ErrNotAttached) {
		return checkpointResult{
			outcome: checkpointUnavailable,
		}
	}
	return checkpointResult{
		outcome: checkpointFailed,
		failure: err,
	}
}

func (checkpointer *liveCheckpointer) remove(
	conversation session.Context,
	lifecycle *checkpointLifecycle,
) {
	checkpointer.mutex.Lock()
	if checkpointer.lifecycles[conversation] == lifecycle {
		delete(checkpointer.lifecycles, conversation)
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
