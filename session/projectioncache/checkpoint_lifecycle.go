package projectioncache

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// checkpointKind identifies the durability path used by an attempt.
type checkpointKind uint8

const (
	// liveCheckpoint flushes the Session before replacing its record.
	liveCheckpoint checkpointKind = iota
	// finalCheckpoint runs after disposal and therefore skips the live Flush.
	finalCheckpoint
)

// checkpointAttempt identifies one execution admitted by checkpointLifecycle.
type checkpointAttempt struct {
	// id rejects a completion belonging to an earlier attempt.
	id uint64
	// kind determines whether the attempt requires a live Session Flush.
	kind checkpointKind
	// coveredEvents is the observed prefix captured when the attempt is admitted.
	coveredEvents uint64
}

// checkpointOutcome is the business classification of an attempt result.
type checkpointOutcome uint8

const (
	// checkpointSucceeded means the checkpoint reached durable record storage.
	checkpointSucceeded checkpointOutcome = iota
	// checkpointFailed means the attempt failed and must be reported.
	checkpointFailed
	// checkpointUnavailable means a live Flush no longer found exact membership.
	checkpointUnavailable
)

// checkpointLifecycleState is the complete checkpoint state for one exact Session.
type checkpointLifecycleState uint8

const (
	// checkpointLifecycleClean has no dirty input, timer, or active attempt.
	checkpointLifecycleClean checkpointLifecycleState = iota
	// checkpointLifecycleDelayed has dirty input and one reserved or installed timer.
	checkpointLifecycleDelayed
	// checkpointLifecycleRunning owns one active live checkpoint attempt.
	checkpointLifecycleRunning
	// checkpointLifecycleQueued coalesces one subsequent live attempt behind the active one.
	checkpointLifecycleQueued
	// checkpointLifecycleFinalQueued waits for the active live attempt before the final one.
	checkpointLifecycleFinalQueued
	// checkpointLifecycleFinalRunning owns the final detached checkpoint attempt.
	checkpointLifecycleFinalRunning
	// checkpointLifecycleClosed is terminal and rejects further scheduling.
	checkpointLifecycleClosed
)

// checkpointLifecycleCommandKind identifies one effect selected by the state machine.
type checkpointLifecycleCommandKind uint8

const (
	// checkpointLifecycleNoop performs no external effect.
	checkpointLifecycleNoop checkpointLifecycleCommandKind = iota
	// checkpointLifecycleArmTimer installs the timer identified by timerGeneration.
	checkpointLifecycleArmTimer
	// checkpointLifecycleStartCheckpoint executes the attached checkpoint attempt.
	checkpointLifecycleStartCheckpoint
	// checkpointLifecycleRemove removes this exact Session from the scheduling index.
	checkpointLifecycleRemove
)

// checkpointLifecycleCommand is the one external effect selected by a state transition.
type checkpointLifecycleCommand struct {
	kind            checkpointLifecycleCommandKind
	timerGeneration uint64
	attempt         checkpointAttempt
}

// checkpointLifecycleEventKind identifies one named input to the state machine.
type checkpointLifecycleEventKind uint8

const (
	// checkpointLifecycleEventAppended observes one committed Session event.
	checkpointLifecycleEventAppended checkpointLifecycleEventKind = iota
	// checkpointLifecycleTimerElapsed observes a timer callback with its reservation identity.
	checkpointLifecycleTimerElapsed
	// checkpointLifecycleSessionDisposed authorizes the final detached checkpoint.
	checkpointLifecycleSessionDisposed
	// checkpointLifecycleCheckpointCompleted observes one admitted attempt result.
	checkpointLifecycleCheckpointCompleted
	// checkpointLifecycleCacheClosing terminates scheduling without forcing a checkpoint.
	checkpointLifecycleCacheClosing
)

// checkpointLifecycleEvent carries one internal state-machine input. Callers use the
// named methods on checkpointLifecycle instead of constructing this object directly.
type checkpointLifecycleEvent struct {
	kind             checkpointLifecycleEventKind
	forceCheckpoint  bool
	writeEveryEvents int
	timerGeneration  uint64
	attempt          checkpointAttempt
	outcome          checkpointOutcome
}

// checkpointLifecycle owns the checkpoint state transitions for one exact Session.
// It performs no checkpoint I/O and starts no goroutine.
type checkpointLifecycle struct {
	mutex sync.Mutex

	// state is the sole source of truth for scheduling and terminal behavior.
	state checkpointLifecycleState
	// observedEvents counts append inputs received for this exact Session.
	observedEvents uint64
	// checkpointedEvents is the greatest prefix covered by a successful attempt.
	checkpointedEvents uint64
	// nextAttemptID allocates a distinct identity to each admitted attempt.
	nextAttemptID uint64
	// activeAttempt is non-zero exactly while state owns an active attempt.
	activeAttempt checkpointAttempt
	// timerGeneration rejects callbacks from canceled or replaced timers.
	timerGeneration uint64
	// timer is the installed runtime resource associated with Delayed state.
	timer *time.Timer
}

func (lifecycle *checkpointLifecycle) eventAppended(
	forceCheckpoint bool,
	writeEveryEvents int,
) (checkpointLifecycleCommand, error) {
	return lifecycle.transition(
		checkpointLifecycleEvent{
			kind:             checkpointLifecycleEventAppended,
			forceCheckpoint:  forceCheckpoint,
			writeEveryEvents: writeEveryEvents,
		},
	)
}

func (lifecycle *checkpointLifecycle) timerElapsed(
	generation uint64,
) (checkpointLifecycleCommand, error) {
	return lifecycle.transition(
		checkpointLifecycleEvent{
			kind:            checkpointLifecycleTimerElapsed,
			timerGeneration: generation,
		},
	)
}

func (lifecycle *checkpointLifecycle) sessionDisposed() (checkpointLifecycleCommand, error) {
	return lifecycle.transition(
		checkpointLifecycleEvent{
			kind: checkpointLifecycleSessionDisposed,
		},
	)
}

func (lifecycle *checkpointLifecycle) checkpointCompleted(
	attempt checkpointAttempt,
	outcome checkpointOutcome,
) (checkpointLifecycleCommand, error) {
	return lifecycle.transition(
		checkpointLifecycleEvent{
			kind:    checkpointLifecycleCheckpointCompleted,
			attempt: attempt,
			outcome: outcome,
		},
	)
}

func (lifecycle *checkpointLifecycle) cacheClosing() (checkpointLifecycleCommand, error) {
	return lifecycle.transition(
		checkpointLifecycleEvent{
			kind: checkpointLifecycleCacheClosing,
		},
	)
}

// installTimer attaches the timer resource created for an ArmTimer command.
// A stale installation is stopped without changing scheduling state.
func (lifecycle *checkpointLifecycle) installTimer(
	generation uint64,
	timer *time.Timer,
) {
	if timer == nil {
		return
	}
	lifecycle.mutex.Lock()
	if generation == 0 ||
		lifecycle.state != checkpointLifecycleDelayed ||
		lifecycle.timerGeneration != generation ||
		lifecycle.timer != nil {
		lifecycle.mutex.Unlock()
		timer.Stop()
		return
	}
	lifecycle.timer = timer
	lifecycle.mutex.Unlock()
}

func (lifecycle *checkpointLifecycle) transition(
	event checkpointLifecycleEvent,
) (checkpointLifecycleCommand, error) {
	lifecycle.mutex.Lock()
	defer lifecycle.mutex.Unlock()
	if err := lifecycle.validate(); err != nil {
		return checkpointLifecycleCommand{}, err
	}
	var command checkpointLifecycleCommand
	var err error
	switch event.kind {
	case checkpointLifecycleEventAppended:
		command, err = lifecycle.transitionEventAppended(event)
	case checkpointLifecycleTimerElapsed:
		command = lifecycle.transitionTimerElapsed(event.timerGeneration)
	case checkpointLifecycleSessionDisposed:
		command, err = lifecycle.transitionSessionDisposed()
	case checkpointLifecycleCheckpointCompleted:
		command, err = lifecycle.transitionCheckpointCompleted(
			event.attempt,
			event.outcome,
		)
	case checkpointLifecycleCacheClosing:
		command = lifecycle.transitionCacheClosing()
	default:
		err = fmt.Errorf(
			"session projection cache: unknown checkpoint lifecycle event %d",
			event.kind,
		)
	}
	if err != nil {
		return checkpointLifecycleCommand{}, err
	}
	if err := lifecycle.validate(); err != nil {
		return checkpointLifecycleCommand{}, err
	}
	return command, nil
}

func (lifecycle *checkpointLifecycle) transitionEventAppended(
	event checkpointLifecycleEvent,
) (checkpointLifecycleCommand, error) {
	if event.writeEveryEvents < 1 {
		return checkpointLifecycleCommand{}, errors.New(
			"session projection cache: writeEveryEvents must be positive",
		)
	}
	switch lifecycle.state {
	case checkpointLifecycleFinalQueued, checkpointLifecycleFinalRunning, checkpointLifecycleClosed:
		return checkpointLifecycleCommand{}, nil
	case checkpointLifecycleClean,
		checkpointLifecycleDelayed,
		checkpointLifecycleRunning,
		checkpointLifecycleQueued:
	default:
		return checkpointLifecycleCommand{}, lifecycle.invalidState(event.kind)
	}
	lifecycle.observedEvents++
	switch lifecycle.state {
	case checkpointLifecycleClean:
		if event.forceCheckpoint || lifecycle.pendingEvents() >= uint64(event.writeEveryEvents) {
			return lifecycle.startAttempt(liveCheckpoint), nil
		}
		lifecycle.state = checkpointLifecycleDelayed
		return lifecycle.armTimer(), nil
	case checkpointLifecycleDelayed:
		if event.forceCheckpoint || lifecycle.pendingEvents() >= uint64(event.writeEveryEvents) {
			lifecycle.cancelTimer()
			return lifecycle.startAttempt(liveCheckpoint), nil
		}
		return checkpointLifecycleCommand{}, nil
	case checkpointLifecycleRunning:
		newEvents := lifecycle.observedEvents - lifecycle.activeAttempt.coveredEvents
		if event.forceCheckpoint || newEvents >= uint64(event.writeEveryEvents) {
			lifecycle.state = checkpointLifecycleQueued
		}
		return checkpointLifecycleCommand{}, nil
	case checkpointLifecycleQueued:
		return checkpointLifecycleCommand{}, nil
	default:
		return checkpointLifecycleCommand{}, lifecycle.invalidState(event.kind)
	}
}

func (lifecycle *checkpointLifecycle) transitionTimerElapsed(generation uint64) checkpointLifecycleCommand {
	if lifecycle.state != checkpointLifecycleDelayed ||
		lifecycle.timerGeneration != generation {
		return checkpointLifecycleCommand{}
	}
	lifecycle.timer = nil
	return lifecycle.startAttempt(liveCheckpoint)
}

func (lifecycle *checkpointLifecycle) transitionSessionDisposed() (checkpointLifecycleCommand, error) {
	switch lifecycle.state {
	case checkpointLifecycleClean, checkpointLifecycleDelayed:
		lifecycle.cancelTimer()
		return lifecycle.startAttempt(finalCheckpoint), nil
	case checkpointLifecycleRunning, checkpointLifecycleQueued:
		lifecycle.state = checkpointLifecycleFinalQueued
		return checkpointLifecycleCommand{}, nil
	case checkpointLifecycleFinalQueued, checkpointLifecycleFinalRunning, checkpointLifecycleClosed:
		return checkpointLifecycleCommand{}, nil
	default:
		return checkpointLifecycleCommand{}, lifecycle.invalidState(checkpointLifecycleSessionDisposed)
	}
}

func (lifecycle *checkpointLifecycle) transitionCheckpointCompleted(
	attempt checkpointAttempt,
	outcome checkpointOutcome,
) (checkpointLifecycleCommand, error) {
	if lifecycle.state == checkpointLifecycleClosed {
		return checkpointLifecycleCommand{}, nil
	}
	if attempt.id == 0 || attempt != lifecycle.activeAttempt {
		return checkpointLifecycleCommand{}, errors.New(
			"session projection cache: checkpoint completion does not match active attempt",
		)
	}
	if outcome > checkpointUnavailable {
		return checkpointLifecycleCommand{}, fmt.Errorf(
			"session projection cache: unknown checkpoint outcome %d",
			outcome,
		)
	}
	if attempt.kind == finalCheckpoint && outcome == checkpointUnavailable {
		return checkpointLifecycleCommand{}, errors.New(
			"session projection cache: final checkpoint cannot be unavailable",
		)
	}
	if outcome == checkpointSucceeded {
		lifecycle.checkpointedEvents = max(
			lifecycle.checkpointedEvents,
			attempt.coveredEvents,
		)
	}
	switch lifecycle.state {
	case checkpointLifecycleRunning:
		lifecycle.activeAttempt = checkpointAttempt{}
		if outcome == checkpointSucceeded && lifecycle.pendingEvents() == 0 {
			lifecycle.state = checkpointLifecycleClean
			return checkpointLifecycleCommand{}, nil
		}
		lifecycle.state = checkpointLifecycleDelayed
		return lifecycle.armTimer(), nil
	case checkpointLifecycleQueued:
		lifecycle.activeAttempt = checkpointAttempt{}
		if outcome == checkpointUnavailable {
			lifecycle.state = checkpointLifecycleDelayed
			return lifecycle.armTimer(), nil
		}
		return lifecycle.startAttempt(liveCheckpoint), nil
	case checkpointLifecycleFinalQueued:
		lifecycle.activeAttempt = checkpointAttempt{}
		return lifecycle.startAttempt(finalCheckpoint), nil
	case checkpointLifecycleFinalRunning:
		lifecycle.activeAttempt = checkpointAttempt{}
		lifecycle.state = checkpointLifecycleClosed
		return checkpointLifecycleCommand{
			kind: checkpointLifecycleRemove,
		}, nil
	case checkpointLifecycleClean, checkpointLifecycleDelayed:
		return checkpointLifecycleCommand{}, lifecycle.invalidState(checkpointLifecycleCheckpointCompleted)
	default:
		return checkpointLifecycleCommand{}, lifecycle.invalidState(checkpointLifecycleCheckpointCompleted)
	}
}

func (lifecycle *checkpointLifecycle) transitionCacheClosing() checkpointLifecycleCommand {
	if lifecycle.state == checkpointLifecycleClosed {
		return checkpointLifecycleCommand{}
	}
	lifecycle.cancelTimer()
	lifecycle.activeAttempt = checkpointAttempt{}
	lifecycle.state = checkpointLifecycleClosed
	return checkpointLifecycleCommand{
		kind: checkpointLifecycleRemove,
	}
}

func (lifecycle *checkpointLifecycle) startAttempt(kind checkpointKind) checkpointLifecycleCommand {
	lifecycle.nextAttemptID++
	if lifecycle.nextAttemptID == 0 {
		lifecycle.nextAttemptID++
	}
	attempt := checkpointAttempt{
		id:            lifecycle.nextAttemptID,
		kind:          kind,
		coveredEvents: lifecycle.observedEvents,
	}
	lifecycle.activeAttempt = attempt
	if kind == finalCheckpoint {
		lifecycle.state = checkpointLifecycleFinalRunning
	} else {
		lifecycle.state = checkpointLifecycleRunning
	}
	return checkpointLifecycleCommand{
		kind:    checkpointLifecycleStartCheckpoint,
		attempt: attempt,
	}
}

func (lifecycle *checkpointLifecycle) armTimer() checkpointLifecycleCommand {
	lifecycle.timerGeneration++
	if lifecycle.timerGeneration == 0 {
		lifecycle.timerGeneration++
	}
	return checkpointLifecycleCommand{
		kind:            checkpointLifecycleArmTimer,
		timerGeneration: lifecycle.timerGeneration,
	}
}

func (lifecycle *checkpointLifecycle) cancelTimer() {
	if lifecycle.timer != nil {
		lifecycle.timer.Stop()
		lifecycle.timer = nil
	}
}

func (lifecycle *checkpointLifecycle) pendingEvents() uint64 {
	return lifecycle.observedEvents - lifecycle.checkpointedEvents
}

func (lifecycle *checkpointLifecycle) validate() error {
	if lifecycle.checkpointedEvents > lifecycle.observedEvents {
		return errors.New(
			"session projection cache: checkpointed events exceed observed events",
		)
	}
	if lifecycle.timer != nil && lifecycle.state != checkpointLifecycleDelayed {
		return errors.New(
			"session projection cache: timer is installed outside delayed state",
		)
	}
	switch lifecycle.state {
	case checkpointLifecycleClean:
		if lifecycle.activeAttempt.id != 0 {
			return errors.New(
				"session projection cache: inactive state owns a checkpoint attempt",
			)
		}
		if lifecycle.pendingEvents() != 0 {
			return errors.New(
				"session projection cache: clean checkpoint lifecycle has pending events",
			)
		}
	case checkpointLifecycleDelayed:
		if lifecycle.activeAttempt.id != 0 {
			return errors.New(
				"session projection cache: delayed state owns a checkpoint attempt",
			)
		}
		if lifecycle.pendingEvents() == 0 || lifecycle.timerGeneration == 0 {
			return errors.New(
				"session projection cache: delayed checkpoint lifecycle has no pending timer",
			)
		}
	case checkpointLifecycleRunning, checkpointLifecycleQueued:
		if lifecycle.activeAttempt.id == 0 ||
			lifecycle.activeAttempt.kind != liveCheckpoint {
			return errors.New(
				"session projection cache: live state does not own a live attempt",
			)
		}
	case checkpointLifecycleFinalQueued:
		if lifecycle.activeAttempt.id == 0 ||
			lifecycle.activeAttempt.kind != liveCheckpoint {
			return errors.New(
				"session projection cache: final queue does not own a live attempt",
			)
		}
	case checkpointLifecycleFinalRunning:
		if lifecycle.activeAttempt.id == 0 ||
			lifecycle.activeAttempt.kind != finalCheckpoint {
			return errors.New(
				"session projection cache: final state does not own a final attempt",
			)
		}
	case checkpointLifecycleClosed:
		if lifecycle.activeAttempt.id != 0 {
			return errors.New(
				"session projection cache: closed state owns a checkpoint attempt",
			)
		}
	default:
		return fmt.Errorf(
			"session projection cache: unknown checkpoint lifecycle state %d",
			lifecycle.state,
		)
	}
	if lifecycle.activeAttempt.id != 0 {
		if lifecycle.activeAttempt.id != lifecycle.nextAttemptID {
			return errors.New(
				"session projection cache: active checkpoint attempt is not current",
			)
		}
		if lifecycle.activeAttempt.coveredEvents > lifecycle.observedEvents {
			return errors.New(
				"session projection cache: active checkpoint covers unobserved events",
			)
		}
	}
	return nil
}

func (lifecycle *checkpointLifecycle) invalidState(event checkpointLifecycleEventKind) error {
	return fmt.Errorf(
		"session projection cache: checkpoint lifecycle event %d is invalid in state %d",
		event,
		lifecycle.state,
	)
}
