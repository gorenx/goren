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
	result, requestErr := conversation.executeCommit(requestContext, plan)
	conversation.lifecycle.finishOperation()
	return result, requestErr
}

func (conversation *sessionContext) executeCommit(
	requestContext context.Context,
	plan WritePlan,
) (result WriteResult, requestErr error) {
	firstSeq := conversation.log.Seq()
	var committed []Event
	defer func() {
		result = conversation.writeResult(firstSeq, committed)
		if recovered := recover(); recovered != nil {
			requestErr = fmt.Errorf("session: WritePlan panicked: %v", recovered)
		}
	}()
	if err := contextCause(requestContext); err != nil {
		return result, err
	}

	var drafts []EventDraft
	var err error
	if fixed, ok := plan.(fixedWritePlan); ok {
		drafts, err = fixed.Build(requestContext, Snapshot{})
	} else {
		drafts, err = plan.Build(requestContext, conversation.log.snapshot())
	}
	if err != nil {
		return result, err
	}
	prepared, err := prepareEventDrafts(drafts)
	if err != nil {
		return result, err
	}
	if err = contextCause(requestContext); err != nil {
		return result, err
	}
	if len(prepared) != 0 {
		committed, err = conversation.log.commitBatch(prepared)
		if err != nil {
			return result, err
		}
	}

	publisher := conversation.lifecycle.appendPublisher()
	if publisher != nil {
		eventContext := conversation.publicationContext(requestContext)
		for _, entry := range committed {
			publisher.Appended(eventContext, conversation, entry)
		}
	}
	return result, nil
}

func (conversation *sessionContext) writeResult(
	firstSeq int64,
	committed []Event,
) WriteResult {
	barrier := conversation.log.currentBarrier()
	return WriteResult{
		FirstSeq: firstSeq,
		NextSeq:  barrier.NextSeq,
		Barrier:  barrier,
		Events:   committed,
	}
}

func contextCause(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("session: Context is nil")
	}
	if requestContext.Err() == nil {
		return nil
	}
	return context.Cause(requestContext)
}

type reentryKey struct{}
