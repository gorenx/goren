package projectioncache

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

type checkpointWriter struct {
	mutex        sync.Mutex
	cache        *sessionCache
	conversation session.Context
	epoch        uint64
	ready        chan struct{}
	schedule     checkpointSchedule
	timer        *time.Timer
}

func newCheckpointWriter(
	cacheOwner *sessionCache,
	conversation session.Context,
	epoch uint64,
) *checkpointWriter {
	return &checkpointWriter{
		cache:        cacheOwner,
		conversation: conversation,
		epoch:        epoch,
		ready:        make(chan struct{}),
	}
}

func (owner *checkpointWriter) pendingEvents() int {
	return owner.schedule.pendingEvents()
}

func (owner *checkpointWriter) advance(committed session.Event) {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.schedule.isRetiring() {
		return
	}
	owner.schedule.observe()
	settings := owner.cache.coordinator
	if committed.Type == session.TurnEndEventName ||
		owner.pendingEvents() >= settings.writeEveryEvents {
		owner.requestWriteLocked()
		return
	}
	if owner.timer == nil {
		owner.armTimerLocked()
	}
}

func (owner *checkpointWriter) retire() {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	owner.schedule.markRetiring()
	owner.requestWriteLocked()
}

func (owner *checkpointWriter) armTimerLocked() {
	if owner.schedule.isRetiring() || owner.timer != nil {
		return
	}
	owner.timer = time.AfterFunc(
		owner.cache.coordinator.writeInterval,
		func() {
			owner.mutex.Lock()
			if owner.timer != nil {
				owner.timer = nil
			}
			owner.requestWriteLocked()
			owner.mutex.Unlock()
		},
	)
}

func (owner *checkpointWriter) requestWriteLocked() {
	if !owner.schedule.requestCheckpoint() {
		return
	}
	if owner.timer != nil {
		owner.timer.Stop()
		owner.timer = nil
	}
	if !owner.cache.coordinator.startAsync(owner.run) {
		owner.schedule.rejectStart()
	}
}

func (owner *checkpointWriter) run() {
	for {
		owner.mutex.Lock()
		if !owner.cache.coordinator.isOpen() || !owner.cache.isCurrent(owner) {
			owner.finishLocked()
			owner.mutex.Unlock()
			owner.cache.removeWriter(owner)
			return
		}
		writeAttempt := owner.schedule.beginAttempt()
		owner.mutex.Unlock()

		err := owner.persistCheckpoint(writeAttempt)
		if err != nil {
			owner.cache.coordinator.report(Failure{
				SessionID: owner.conversation.ID(),
				Operation: "live checkpoint",
				Error:     err,
			})
		}

		owner.mutex.Lock()
		nextAction := owner.schedule.finishAttempt(
			writeAttempt,
			err == nil,
			owner.cache.coordinator.isOpen() && owner.cache.isCurrent(owner),
		)
		switch nextAction {
		case checkpointContinue:
			owner.mutex.Unlock()
			continue
		case checkpointRetry:
			owner.armTimerLocked()
			owner.mutex.Unlock()
			return
		case checkpointIdle:
			owner.mutex.Unlock()
			return
		case checkpointRemove:
			owner.stopTimerLocked()
			owner.mutex.Unlock()
			owner.cache.removeWriter(owner)
			return
		}
	}
}

func (owner *checkpointWriter) finishLocked() {
	owner.schedule.stop()
	owner.stopTimerLocked()
}

func (owner *checkpointWriter) stopTimerLocked() {
	if owner.timer != nil {
		owner.timer.Stop()
		owner.timer = nil
	}
}

func (owner *checkpointWriter) stop() {
	owner.mutex.Lock()
	if owner.timer != nil {
		owner.timer.Stop()
		owner.timer = nil
	}
	remove := !owner.schedule.isWriting()
	owner.mutex.Unlock()
	if remove {
		owner.cache.removeWriter(owner)
	}
}

func (owner *checkpointWriter) persistCheckpoint(
	writeAttempt checkpointAttempt,
) error {
	cacheCoordinator := owner.cache.coordinator
	rows, err := cacheCoordinator.projections.Checkpoint(owner.conversation)
	if err != nil {
		return err
	}
	if _, err := checkpointCut(rows, owner.conversation.Seq()-1); err != nil {
		return err
	}
	metadata := owner.conversation.Header()
	if writeAttempt.requiresLiveFlush() {
		if err := cacheCoordinator.sessions.Flush(
			context.Background(),
			owner.conversation,
		); err != nil {
			return err
		}
	}
	if err := owner.cache.record.replaceLive(
		context.Background(),
		owner.epoch,
		&owner.cache.currentEpoch,
		CheckpointRecord{
			Identity: identityOf(metadata),
			Rows:     rows,
		},
	); err != nil {
		return err
	}
	return nil
}

func checkpointCut(
	rows sessionprojection.Checkpoint,
	emptyCut int64,
) (int64, error) {
	if len(rows) == 0 {
		return emptyCut, nil
	}
	var cut int64
	first := true
	for _, row := range rows {
		if first {
			cut = row.Seq
			first = false
			continue
		}
		if row.Seq != cut {
			return -1, errors.New("session projection cache: checkpoint rows do not share one event cut")
		}
	}
	return cut, nil
}
