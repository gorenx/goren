package session

import (
	"context"
	"errors"
	"fmt"
	"reflect"
)

// Commit owns the atomic business transaction without lifecycle or publication.
// The caller serializes all Log operations, including detached reads.
func (conversation *Log) Commit(
	requestContext context.Context,
	plan WritePlan,
) (result WriteResult, commitErr error) {
	if conversation == nil {
		return WriteResult{}, errors.New("session: write to nil log")
	}
	firstSeq := conversation.Seq()
	var committed []Event
	var replay *WriteResult
	defer func() {
		barrier := conversation.Barrier()
		result = WriteResult{
			FirstSeq: firstSeq,
			NextSeq:  barrier.NextSeq,
			Barrier:  barrier,
			Events:   committed,
		}
		if replay != nil && commitErr == nil {
			result = *replay
		}
		if recovered := recover(); recovered != nil {
			commitErr = fmt.Errorf("session: WritePlan panicked: %v", recovered)
		}
	}()
	if err := contextCause(requestContext); err != nil {
		return result, err
	}
	if plan == nil {
		return result, errors.New("session: WritePlan is nil")
	}
	var drafts []EventDraft
	var err error
	if fixed, ok := plan.(fixedWritePlan); ok {
		drafts, err = fixed.Build(requestContext, Snapshot{})
	} else {
		snapshot := conversation.Snapshot()
		if retry, ok := plan.(IdempotentWrite); ok {
			previous, failure := retry.FindCommit(snapshot)
			if failure != nil {
				return result, failure
			}
			if previous != nil {
				if err := contextCause(requestContext); err != nil {
					return result, err
				}
				replay, failure = conversation.replayCommit(*previous)
				return result, failure
			}
		}
		drafts, err = plan.Build(requestContext, snapshot)
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
		committed, err = conversation.commitBatch(prepared)
	}
	return result, err
}

// replayCommit accepts only an exact existing batch and returns detached data.
func (conversation *Log) replayCommit(previous WriteResult) (*WriteResult, error) {
	if previous.Barrier.SessionID != conversation.header.ID || previous.FirstSeq < 0 ||
		previous.NextSeq <= previous.FirstSeq || previous.NextSeq > conversation.Seq() ||
		previous.Barrier.NextSeq != previous.NextSeq || int64(len(previous.Events)) != previous.NextSeq-previous.FirstSeq {
		return nil, errors.New("session: invalid replayed commit range")
	}
	copied := make([]Event, len(previous.Events))
	for index, entry := range previous.Events {
		stored := conversation.entries[previous.FirstSeq+int64(index)]
		if !reflect.DeepEqual(entry, stored) {
			return nil, errors.New("session: replayed event differs from committed log")
		}
		copied[index] = cloneEvent(stored)
	}
	previous.Events = copied
	return &previous, nil
}
