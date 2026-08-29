package projectioncache

import (
	"sync"
	"time"
)

// checkpointKind identifies whether an attempt still belongs to a live Session.
type checkpointKind uint8

const (
	// liveCheckpoint flushes the Session before replacing its record.
	liveCheckpoint checkpointKind = iota
	// finalCheckpoint runs after detach and therefore skips the live Flush.
	finalCheckpoint
)

type checkpointAttempt struct {
	// kind determines whether the attempt requires a live Session Flush.
	kind checkpointKind
	// coveredEvents is the observed prefix captured when the attempt begins.
	coveredEvents uint64
}

// liveWriteAction tells liveCheckpointer what to do after one attempt.
type liveWriteAction uint8

const (
	// liveWriteIdle retains clean state and waits for another event.
	liveWriteIdle liveWriteAction = iota
	// liveWriteContinue immediately executes the coalesced next attempt.
	liveWriteContinue
	// liveWriteRetry schedules a delayed attempt for remaining dirty events.
	liveWriteRetry
	// liveWriteRemove deletes terminal or stopped scheduling state.
	liveWriteRemove
)

// liveWritePhase is the complete scheduling phase for one exact Session.
type liveWritePhase uint8

const (
	// liveWritePhaseIdle has no active attempt or timer.
	liveWritePhaseIdle liveWritePhase = iota
	// liveWritePhaseWaiting has one reserved interval timer.
	liveWritePhaseWaiting
	// liveWritePhaseWriting owns one active live checkpoint attempt.
	liveWritePhaseWriting
	// liveWritePhaseRerun coalesces a trigger received during a live attempt.
	liveWritePhaseRerun
	// liveWritePhaseFinalPending waits for the active live attempt before final write.
	liveWritePhaseFinalPending
	// liveWritePhaseFinalWriting owns the one detached final attempt.
	liveWritePhaseFinalWriting
	// liveWritePhaseStopped rejects scheduling during cache shutdown.
	liveWritePhaseStopped
)

type timerRequest struct {
	// generation is non-zero only when liveCheckpointer must install a timer.
	generation uint64
}

// liveWrite is the scheduling state for one exact live Session. It owns its
// mutex and timer identity, but performs no I/O and starts no goroutine.
type liveWrite struct {
	mutex sync.Mutex

	// phase is the sole source of truth for timer, attempt, rerun, and detach state.
	phase liveWritePhase
	// observedEvents counts append inputs received for this exact Session.
	observedEvents uint64
	// checkpointed is the greatest observed prefix covered by a successful attempt.
	checkpointed uint64
	// timer is non-nil after a reserved timer has been installed.
	timer *time.Timer
	// timerGeneration rejects callbacks from canceled or replaced timers.
	timerGeneration uint64
}

func (write *liveWrite) observe(
	forceCheckpoint bool,
	writeEveryEvents int,
) (bool, timerRequest) {
	write.mutex.Lock()
	defer write.mutex.Unlock()
	switch write.phase {
	case liveWritePhaseFinalPending,
		liveWritePhaseFinalWriting,
		liveWritePhaseStopped:
		return false, timerRequest{}
	}
	write.observedEvents++
	if forceCheckpoint || write.pendingEvents() >= writeEveryEvents {
		switch write.phase {
		case liveWritePhaseIdle,
			liveWritePhaseWaiting:
			write.cancelTimer()
			write.phase = liveWritePhaseWriting
			return true, timerRequest{}
		case liveWritePhaseWriting:
			write.phase = liveWritePhaseRerun
		}
		return false, timerRequest{}
	}
	if write.phase == liveWritePhaseIdle {
		write.phase = liveWritePhaseWaiting
		return false, write.reserveTimer()
	}
	return false, timerRequest{}
}

func (write *liveWrite) detach() bool {
	write.mutex.Lock()
	defer write.mutex.Unlock()
	switch write.phase {
	case liveWritePhaseIdle,
		liveWritePhaseWaiting:
		write.cancelTimer()
		write.phase = liveWritePhaseFinalWriting
		return true
	case liveWritePhaseWriting,
		liveWritePhaseRerun:
		write.phase = liveWritePhaseFinalPending
		return false
	case liveWritePhaseFinalPending,
		liveWritePhaseFinalWriting,
		liveWritePhaseStopped:
		return false
	default:
		return false
	}
}

func (write *liveWrite) beginAttempt() checkpointAttempt {
	write.mutex.Lock()
	defer write.mutex.Unlock()
	kind := liveCheckpoint
	switch write.phase {
	case liveWritePhaseRerun:
		write.phase = liveWritePhaseWriting
	case liveWritePhaseFinalPending:
		write.phase = liveWritePhaseFinalWriting
		kind = finalCheckpoint
	case liveWritePhaseFinalWriting:
		kind = finalCheckpoint
	}
	return checkpointAttempt{
		kind:          kind,
		coveredEvents: write.observedEvents,
	}
}

func (write *liveWrite) finishAttempt(
	attempt checkpointAttempt,
	succeeded bool,
) liveWriteAction {
	write.mutex.Lock()
	defer write.mutex.Unlock()
	if succeeded {
		write.checkpointed = max(write.checkpointed, attempt.coveredEvents)
	}
	switch write.phase {
	case liveWritePhaseStopped:
		return liveWriteRemove
	case liveWritePhaseFinalWriting:
		write.phase = liveWritePhaseStopped
		return liveWriteRemove
	case liveWritePhaseFinalPending:
		return liveWriteContinue
	case liveWritePhaseRerun:
		return liveWriteContinue
	case liveWritePhaseWriting:
		write.phase = liveWritePhaseIdle
		if write.pendingEvents() == 0 {
			return liveWriteIdle
		}
		return liveWriteRetry
	default:
		write.phase = liveWritePhaseStopped
		return liveWriteRemove
	}
}

func (write *liveWrite) retryTimer() timerRequest {
	write.mutex.Lock()
	defer write.mutex.Unlock()
	if write.phase != liveWritePhaseIdle {
		return timerRequest{}
	}
	write.phase = liveWritePhaseWaiting
	return write.reserveTimer()
}

func (write *liveWrite) installTimer(
	request timerRequest,
	timer *time.Timer,
) {
	write.mutex.Lock()
	if request.generation == 0 ||
		write.phase != liveWritePhaseWaiting ||
		write.timerGeneration != request.generation ||
		write.timer != nil {
		write.mutex.Unlock()
		timer.Stop()
		return
	}
	write.timer = timer
	write.mutex.Unlock()
}

func (write *liveWrite) timerElapsed(generation uint64) bool {
	write.mutex.Lock()
	defer write.mutex.Unlock()
	if write.phase != liveWritePhaseWaiting ||
		write.timerGeneration != generation {
		return false
	}
	write.timer = nil
	write.phase = liveWritePhaseWriting
	return true
}

func (write *liveWrite) stop() bool {
	write.mutex.Lock()
	defer write.mutex.Unlock()
	active := write.phase == liveWritePhaseWriting ||
		write.phase == liveWritePhaseRerun ||
		write.phase == liveWritePhaseFinalPending ||
		write.phase == liveWritePhaseFinalWriting
	write.cancelTimer()
	write.phase = liveWritePhaseStopped
	return !active
}

func (write *liveWrite) state() (int, bool) {
	write.mutex.Lock()
	defer write.mutex.Unlock()
	active := write.phase == liveWritePhaseWriting ||
		write.phase == liveWritePhaseRerun ||
		write.phase == liveWritePhaseFinalPending ||
		write.phase == liveWritePhaseFinalWriting
	return write.pendingEvents(), active
}

func (write *liveWrite) pendingEvents() int {
	return int(write.observedEvents - write.checkpointed)
}

func (write *liveWrite) reserveTimer() timerRequest {
	write.timerGeneration++
	return timerRequest{
		generation: write.timerGeneration,
	}
}

func (write *liveWrite) cancelTimer() {
	if write.timer != nil {
		write.timer.Stop()
		write.timer = nil
	}
}
