package sessionpersistence

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gorenx/goren/session"
)

type liveWriter struct {
	mutex     sync.Mutex
	pending   []session.Event
	timer     *time.Timer
	writing   bool
	writeDone chan struct{}
	paused    bool
	expired   bool
	delay     time.Duration
	write     func(context.Context, []session.Event) error
	report    func(error)
}

func newLiveWriter(
	delay time.Duration,
	write func(context.Context, []session.Event) error,
	report func(error),
) *liveWriter {
	return &liveWriter{delay: delay, write: write, report: report}
}

func (owner *liveWriter) enqueue(committed session.Event) {
	owner.mutex.Lock()
	wasEmpty := len(owner.pending) == 0
	owner.pending = append(owner.pending, snapshotEvents([]session.Event{committed})[0])
	if owner.paused {
		owner.paused = false
		owner.expired = false
		owner.armLocked()
	} else if wasEmpty && !owner.writing {
		owner.armLocked()
	} else if wasEmpty && owner.writing {
		owner.armLocked()
	}
	owner.mutex.Unlock()
}

func (owner *liveWriter) hasWork() bool {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	return len(owner.pending) != 0 || owner.writing
}

func (owner *liveWriter) flush(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("session persistence: flush Context is nil")
	}
	for {
		owner.mutex.Lock()
		owner.cancelTimerLocked()
		owner.expired = false
		owner.paused = false
		if owner.writing {
			done := owner.writeDone
			owner.mutex.Unlock()
			select {
			case <-done:
				continue
			case <-requestContext.Done():
				return context.Cause(requestContext)
			}
		}
		if len(owner.pending) == 0 {
			owner.mutex.Unlock()
			return nil
		}
		batch, done := owner.startWriteLocked()
		owner.mutex.Unlock()

		writeErr := owner.write(requestContext, batch)
		owner.finishWrite(batch, done, writeErr, false)
		if writeErr != nil {
			return writeErr
		}
	}
}

func (owner *liveWriter) armLocked() {
	if owner.timer != nil || len(owner.pending) == 0 {
		return
	}
	owner.timer = time.AfterFunc(owner.delay, owner.onDeadline)
}

func (owner *liveWriter) cancelTimerLocked() {
	if owner.timer == nil {
		return
	}
	owner.timer.Stop()
	owner.timer = nil
}

func (owner *liveWriter) onDeadline() {
	owner.mutex.Lock()
	owner.timer = nil
	if owner.writing {
		owner.expired = true
		owner.mutex.Unlock()
		return
	}
	if len(owner.pending) == 0 || owner.paused {
		owner.mutex.Unlock()
		return
	}
	batch, done := owner.startWriteLocked()
	owner.mutex.Unlock()

	writeErr := owner.write(context.Background(), batch)
	owner.finishWrite(batch, done, writeErr, true)
}

func (owner *liveWriter) startWriteLocked() ([]session.Event, chan struct{}) {
	batch := owner.pending
	owner.pending = nil
	owner.cancelTimerLocked()
	owner.expired = false
	owner.writing = true
	done := make(chan struct{})
	owner.writeDone = done
	return batch, done
}

func (owner *liveWriter) finishWrite(batch []session.Event, done chan struct{}, writeErr error, background bool) {
	owner.mutex.Lock()
	if writeErr != nil {
		retained := make([]session.Event, 0, len(batch)+len(owner.pending))
		retained = append(retained, batch...)
		retained = append(retained, owner.pending...)
		owner.pending = retained
		owner.paused = true
		owner.expired = false
	}
	owner.writing = false
	owner.writeDone = nil
	continueImmediately := writeErr == nil && owner.expired && len(owner.pending) != 0
	owner.expired = false
	close(done)
	owner.mutex.Unlock()

	if background && writeErr != nil {
		owner.safeReport(writeErr)
	}
	if continueImmediately {
		go owner.onDeadline()
	}
}

func (owner *liveWriter) safeReport(problem error) {
	defer func() { _ = recover() }()
	owner.report(problem)
}
