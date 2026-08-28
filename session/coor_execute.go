package session

import (
	"context"
	"fmt"
)

func (owner *coordinator) executeRequest(
	item *request,
) (result WriteResult, requestErr error) {
	firstSeq := owner.log.Seq()
	var committed []Event
	defer func() {
		result = owner.writeResult(firstSeq, committed)
		if recovered := recover(); recovered != nil {
			requestErr = fmt.Errorf("session: WritePlan panicked: %v", recovered)
		}
	}()
	if err := contextCause(item.requestContext); err != nil {
		return result, err
	}
	if item.kind != requestCommit || item.plan == nil {
		return result, fmt.Errorf("session: invalid coordinator request")
	}

	var drafts []EventDraft
	var err error
	if fixed, ok := item.plan.(fixedWritePlan); ok {
		drafts, err = fixed.Build(item.requestContext, Snapshot{})
	} else {
		current := owner.log.snapshot()
		drafts, err = item.plan.Build(item.requestContext, current)
	}
	if err != nil {
		return result, err
	}
	prepared, err := prepareEventDrafts(drafts)
	if err != nil {
		return result, err
	}
	if err = contextCause(item.requestContext); err != nil {
		return result, err
	}
	if len(prepared) != 0 {
		committed, err = owner.log.commitBatch(prepared)
		if err != nil {
			return result, err
		}
	}

	eventContext := context.WithValue(
		item.requestContext,
		reentryKey{},
		owner,
	)
	owner.queue.mutex.Lock()
	store := owner.store
	shouldPublish := owner.machine.publishesEvents()
	owner.queue.mutex.Unlock()
	if store != nil && shouldPublish {
		for _, entry := range committed {
			store.publishAppend(eventContext, owner, entry)
		}
	}
	return result, nil
}

func (owner *coordinator) writeResult(firstSeq int64, committed []Event) WriteResult {
	barrier := owner.log.currentBarrier()
	return WriteResult{
		FirstSeq: firstSeq,
		NextSeq:  barrier.NextSeq,
		Barrier:  barrier,
		Events:   committed,
	}
}

type reentryKey struct{}
