package llmretry

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
)

// retryWrite is an immutable retry fact checked against the serialized Session log.
type retryWrite struct {
	sessionID session.SessionID
	chain     RetryID
	number    int64
	draft     session.EventDraft
	scheduled RetryRecord
	started   *RetryStarted
}

func (plan *retryWrite) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	if err := checkSnapshot(snapshot, plan.sessionID); err != nil {
		return nil, err
	}
	history, err := analyzeHistory(snapshot.Events)
	if err != nil {
		return nil, err
	}
	if plan.started != nil {
		if !history.turnOpen || !history.stepOpen || history.turn != plan.started.Turn || history.stepTurn != plan.started.Turn || history.step != plan.started.Step {
			return nil, errors.New("llm-retry: retry no longer belongs to the open step")
		}
		err = history.addStarted(*plan.started)
	} else {
		err = history.addRetry(plan.scheduled)
	}
	if err != nil {
		return nil, err
	}
	return []session.EventDraft{plan.draft}, nil
}

func (plan *retryWrite) FindCommit(snapshot session.Snapshot) (*session.WriteResult, error) {
	if err := checkSnapshot(snapshot, plan.sessionID); err != nil {
		return nil, err
	}
	var found *session.WriteResult
	for _, entry := range snapshot.Events {
		var chain RetryID
		var number int64
		if plan.started != nil && entry.Type == RetryStartedEventName {
			record, err := DecodeRetryStarted(entry.Data)
			if err != nil {
				return nil, err
			}
			chain, number = record.RetryID, record.Retry
		} else if plan.started == nil && entry.Type == RetryEventName {
			record, err := DecodeRetryRecord(entry.Data)
			if err != nil {
				return nil, err
			}
			facts, err := factsFromRecord(record)
			if err != nil {
				return nil, err
			}
			chain, number = facts.chainID, facts.retry
		} else {
			continue
		}
		if chain != plan.chain || number != plan.number {
			continue
		}
		if found != nil {
			return nil, errors.New("llm-retry: duplicate committed retry fact")
		}
		if err := plan.draft.ValidateEvent(entry); err != nil {
			return nil, err
		}
		found = &session.WriteResult{
			FirstSeq: entry.Seq,
			NextSeq:  entry.Seq + 1,
			Events:   []session.Event{entry},
			Barrier: session.WriteBarrier{
				SessionID: plan.sessionID,
				NextSeq:   entry.Seq + 1,
			},
		}
	}
	return found, nil
}

func checkSnapshot(snapshot session.Snapshot, identifier session.SessionID) error {
	if snapshot.Barrier.SessionID != identifier || snapshot.Barrier.NextSeq != int64(len(snapshot.Events)) {
		return errors.New("llm-retry: complete matching Session snapshot is required")
	}
	for index, entry := range snapshot.Events {
		if entry.Seq != int64(index) {
			return errors.New("llm-retry: Session sequence is discontinuous")
		}
	}
	return nil
}
