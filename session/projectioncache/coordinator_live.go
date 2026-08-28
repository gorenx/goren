package projectioncache

import (
	"errors"

	"github.com/gorenx/goren/session"
)

// Begin establishes one exact Session lifecycle before append or disposal
// events can schedule checkpoint work.
func (owner *Coordinator) Begin(conversation session.Context) error {
	if conversation == nil {
		return errors.New("session projection cache: Begin Session is nil")
	}
	checkpoint, leave, err := owner.enterSession(conversation.ID())
	if errors.Is(err, ErrClosed) {
		return nil
	}
	if err != nil {
		return err
	}
	defer leave()
	checkpoint.writerFor(conversation)
	return nil
}

// Advance records one committed live Session event and schedules checkpoint
// work without performing storage I/O on the Event observer call.
func (owner *Coordinator) Advance(
	conversation session.Context,
	committed session.Event,
) error {
	if conversation == nil {
		return errors.New("session projection cache: Advance Session is nil")
	}
	checkpoint, leave, err := owner.enterSession(conversation.ID())
	if errors.Is(err, ErrClosed) {
		return nil
	}
	if err != nil {
		return err
	}
	defer leave()
	checkpoint.writerFor(conversation).advance(committed)
	return nil
}

// Retire requests the final checkpoint for one exact Session lifecycle.
func (owner *Coordinator) Retire(conversation session.Context) error {
	if conversation == nil {
		return errors.New("session projection cache: Retire Session is nil")
	}
	checkpoint, leave, err := owner.enterSession(conversation.ID())
	if errors.Is(err, ErrClosed) {
		return nil
	}
	if err != nil {
		return err
	}
	defer leave()
	checkpoint.writerFor(conversation).retire()
	return nil
}
