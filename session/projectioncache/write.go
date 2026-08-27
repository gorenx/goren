package projectioncache

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

// Advance records one committed live Session event and schedules checkpoint
// work without performing storage I/O on the Event observer call.
func (owner *CheckpointCache) Advance(
	conversation session.Context,
	committed session.Event,
) error {
	if conversation == nil {
		return errors.New("session projection cache: Advance Session is nil")
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.state != cacheOpen {
		return nil
	}
	state := owner.writeStateLocked(conversation)
	if state.retiring {
		return nil
	}
	state.latestSeq = max(state.latestSeq, committed.Seq)
	state.generation++
	if committed.Type == session.TurnEndEventName ||
		state.pendingEvents() >= owner.writeEveryEvents {
		owner.requestWriteLocked(state)
		return nil
	}
	if state.timer == nil {
		owner.armTimerLocked(state)
	}
	return nil
}

// Retire requests the final checkpoint for one exact Session lifecycle.
func (owner *CheckpointCache) Retire(conversation session.Context) error {
	if conversation == nil {
		return errors.New("session projection cache: Retire Session is nil")
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.state != cacheOpen {
		return nil
	}
	state := owner.writeStateLocked(conversation)
	state.latestSeq = max(state.latestSeq, conversation.Seq()-1)
	state.retiring = true
	owner.requestWriteLocked(state)
	return nil
}

func (owner *CheckpointCache) armTimerLocked(state *writeState) {
	if owner.state != cacheOpen || state.retiring || state.timer != nil {
		return
	}
	state.timer = time.AfterFunc(
		owner.writeInterval,
		func() {
			owner.mutex.Lock()
			if state.timer != nil {
				state.timer = nil
			}
			if owner.state == cacheOpen && owner.writes[state.conversation] == state {
				owner.requestWriteLocked(state)
			}
			owner.mutex.Unlock()
		},
	)
}

func (owner *CheckpointCache) writeStateLocked(
	conversation session.Context,
) *writeState {
	selected := owner.writes[conversation]
	if selected == nil {
		selected = &writeState{
			conversation: conversation,
			latestSeq:    -1,
			persistedSeq: -1,
		}
		owner.writes[conversation] = selected
	}
	return selected
}

func (owner *CheckpointCache) requestWriteLocked(state *writeState) {
	if owner.state != cacheOpen {
		return
	}
	if state.writing {
		state.requested = true
		return
	}
	state.writing = true
	state.requested = false
	if state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
	owner.inflight.Add(1)
	go owner.runWriter(state)
}

func (owner *CheckpointCache) runWriter(state *writeState) {
	defer owner.inflight.Done()
	for {
		owner.mutex.Lock()
		if owner.state != cacheOpen {
			owner.finishWriterLocked(state, true)
			owner.mutex.Unlock()
			return
		}
		targetGeneration := state.generation
		state.requested = false
		owner.mutex.Unlock()

		cut, err := owner.persistCheckpoint(state.conversation)
		if err != nil {
			owner.report(Failure{
				SessionID: state.conversation.ID(),
				Operation: "live checkpoint",
				Error:     err,
			})
		}

		owner.mutex.Lock()
		if err == nil {
			state.persistedGen = max(state.persistedGen, targetGeneration)
			state.persistedSeq = max(state.persistedSeq, cut)
		}
		closing := owner.state != cacheOpen
		if closing || err != nil {
			owner.finishWriterLocked(state, state.retiring || closing)
			if err != nil && !state.retiring && !closing && state.pendingEvents() != 0 {
				owner.armTimerLocked(state)
			}
			owner.mutex.Unlock()
			return
		}
		needsAnother := state.retiring && state.persistedSeq < state.latestSeq
		needsAnother = needsAnother || state.requested
		if needsAnother {
			owner.mutex.Unlock()
			continue
		}
		owner.finishWriterLocked(state, state.retiring)
		if !state.retiring && state.pendingEvents() != 0 {
			owner.armTimerLocked(state)
		}
		owner.mutex.Unlock()
		return
	}
}

func (owner *CheckpointCache) finishWriterLocked(
	state *writeState,
	remove bool,
) {
	state.writing = false
	state.requested = false
	if remove && owner.writes[state.conversation] == state {
		if state.timer != nil {
			state.timer.Stop()
			state.timer = nil
		}
		delete(owner.writes, state.conversation)
	}
}

func (owner *CheckpointCache) persistCheckpoint(
	conversation session.Context,
) (int64, error) {
	rows, err := owner.projections.Checkpoint(conversation)
	if err != nil {
		return -1, err
	}
	cut, err := checkpointCut(rows, conversation.Seq()-1)
	if err != nil {
		return -1, err
	}
	metadata := conversation.Header()
	if live, found := owner.sessions.Get(metadata.ID); found && live == conversation {
		if err := owner.sessions.Flush(context.Background(), conversation); err != nil {
			return -1, err
		}
	}
	if err := owner.replaceCheckpoint(
		context.Background(),
		metadata,
		rows,
	); err != nil {
		return -1, err
	}
	return cut, nil
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
