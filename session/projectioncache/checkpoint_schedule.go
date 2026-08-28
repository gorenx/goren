package projectioncache

type checkpointWriteKind uint8

const (
	liveCheckpointWrite checkpointWriteKind = iota
	finalCheckpointWrite
)

type checkpointAttempt struct {
	kind          checkpointWriteKind
	coveredEvents uint64
}

func (attempt checkpointAttempt) requiresLiveFlush() bool {
	return attempt.kind == liveCheckpointWrite
}

type checkpointCompletion uint8

const (
	checkpointIdle checkpointCompletion = iota
	checkpointContinue
	checkpointRetry
	checkpointRemove
)

// checkpointSchedule is the lock-free decision state for one exact lifecycle
// writer. checkpointWriter serializes calls and performs the resulting I/O.
type checkpointSchedule struct {
	// observedEvents counts committed events observed by this exact lifecycle.
	observedEvents uint64
	// checkpointedEvents is the observed prefix covered by a successful write.
	checkpointedEvents uint64
	// writing means one writer goroutine currently owns checkpoint attempts.
	writing bool
	// rerunRequested preserves a trigger that arrived during an active attempt.
	rerunRequested bool
	// retiring makes the next attempt the one-shot detached final checkpoint.
	retiring bool
}

func (schedule *checkpointSchedule) observe() {
	schedule.observedEvents++
}

func (schedule *checkpointSchedule) pendingEvents() int {
	return int(schedule.observedEvents - schedule.checkpointedEvents)
}

func (schedule *checkpointSchedule) markRetiring() {
	schedule.retiring = true
}

func (schedule *checkpointSchedule) isRetiring() bool {
	return schedule.retiring
}

// requestCheckpoint records the trigger and reports whether a writer must be
// started. A trigger arriving during I/O becomes an immediate rerun request.
func (schedule *checkpointSchedule) requestCheckpoint() bool {
	if schedule.writing {
		schedule.rerunRequested = true
		return false
	}
	schedule.writing = true
	schedule.rerunRequested = false
	return true
}

func (schedule *checkpointSchedule) rejectStart() {
	schedule.writing = false
}

func (schedule *checkpointSchedule) beginAttempt() checkpointAttempt {
	kind := liveCheckpointWrite
	if schedule.retiring {
		kind = finalCheckpointWrite
	}
	schedule.rerunRequested = false
	return checkpointAttempt{
		kind:          kind,
		coveredEvents: schedule.observedEvents,
	}
}

func (schedule *checkpointSchedule) finishAttempt(
	completed checkpointAttempt,
	succeeded bool,
	operational bool,
) checkpointCompletion {
	if succeeded {
		schedule.checkpointedEvents = max(
			schedule.checkpointedEvents,
			completed.coveredEvents,
		)
	}
	if !operational || completed.kind == finalCheckpointWrite {
		schedule.stop()
		return checkpointRemove
	}
	if schedule.retiring || schedule.rerunRequested {
		return checkpointContinue
	}
	schedule.writing = false
	if schedule.pendingEvents() == 0 {
		return checkpointIdle
	}
	return checkpointRetry
}

func (schedule *checkpointSchedule) stop() {
	schedule.writing = false
	schedule.rerunRequested = false
}

func (schedule *checkpointSchedule) isWriting() bool {
	return schedule.writing
}
