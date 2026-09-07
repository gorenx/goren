package session

import (
	"context"
	"errors"
	"fmt"
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
	defer func() {
		barrier := conversation.Barrier()
		result = WriteResult{
			FirstSeq: firstSeq,
			NextSeq:  barrier.NextSeq,
			Barrier:  barrier,
			Events:   committed,
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
		drafts, err = plan.Build(requestContext, conversation.Snapshot())
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
