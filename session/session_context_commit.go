package session

import (
	"context"
	"errors"
	"fmt"
)

// Commit admits one plan to this Session's sole FIFO. The plan builds against
// the snapshot at the FIFO head; the complete batch commits atomically before
// any live event notification is emitted.
func (conversation *sessionContext) Commit(
	requestContext context.Context,
	plan WritePlan,
) (WriteResult, error) {
	if conversation == nil {
		return WriteResult{}, errors.New("session: write to nil Session")
	}
	if requestContext == nil {
		return WriteResult{}, errors.New("session: write Context is nil")
	}
	if plan == nil {
		return WriteResult{}, errors.New("session: WritePlan is nil")
	}
	if err := contextCause(requestContext); err != nil {
		return WriteResult{}, err
	}
	if conversation.isReentry(requestContext) {
		return WriteResult{}, ErrWriteReentry
	}
	if err := conversation.lifecycle.beginCommit(); err != nil {
		return WriteResult{}, err
	}
	defer conversation.lifecycle.finishOperation()
	result, requestErr := conversation.log.Commit(requestContext, plan)
	if requestErr != nil {
		return result, requestErr
	}
	return result, conversation.publishCommitted(requestContext, result.Events)
}

func (conversation *sessionContext) publishCommitted(ctx context.Context, committed []Event) (failure error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			failure = fmt.Errorf("session: append publication panicked: %v", recovered)
		}
	}()
	publisher := conversation.lifecycle.appendPublisher()
	if publisher != nil {
		eventContext := conversation.publicationContext(ctx)
		for _, entry := range committed {
			publisher.Appended(eventContext, conversation, entry)
		}
	}
	return nil
}

type reentryKey struct{}
