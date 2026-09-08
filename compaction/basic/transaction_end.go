package basic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/session"
)

// transactionEnd proposes a failed end for one already-started compaction.
// Successful completion must commit the summary, replacement, and end together.
type transactionEnd struct {
	lifecycle compaction.Start
	// detail fixes the persisted diagnostic; the caller retains the original error.
	detail string
}

func newTransactionEnd(lifecycle compaction.Start, failure error) (*transactionEnd, error) {
	if failure == nil {
		return nil, errors.New("compaction: failed transaction end requires a cause")
	}
	encoded, err := json.Marshal(lifecycle)
	if err != nil {
		return nil, err
	}
	detached, err := compaction.DecodeStart(encoded)
	if err != nil {
		return nil, err
	}
	return &transactionEnd{
		lifecycle: detached,
		detail:    failure.Error(),
	}, nil
}

func (proposal *transactionEnd) draft() (session.EventDraft, error) {
	return session.NewEventDraft(compaction.EndEvent, compaction.End{
		CompactionID:    proposal.lifecycle.CompactionID,
		SourceCommandID: proposal.lifecycle.SourceCommandID,
		Turn:            proposal.lifecycle.Turn,
		Error:           &proposal.detail,
	})
}

func (proposal *transactionEnd) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	state, err := compaction.InspectLog(snapshot.Events)
	if err != nil {
		return nil, err
	}
	if state.Attempt == nil || state.Attempt.CompactionID != proposal.lifecycle.CompactionID {
		return nil, errors.New("compaction: failed end does not match the active transaction")
	}
	sequence := state.Attempt.StartSeq
	if sequence < 0 || sequence >= int64(len(snapshot.Events)) {
		return nil, errors.New("compaction: active transaction has no start event")
	}
	start, err := session.NewEventDraft(compaction.StartEvent, proposal.lifecycle)
	if err != nil {
		return nil, err
	}
	if err := start.CheckCommit(snapshot.Events[sequence]); err != nil {
		return nil, fmt.Errorf("compaction: failed end conflicts with transaction start: %w", err)
	}
	draft, err := proposal.draft()
	if err != nil {
		return nil, err
	}
	return []session.EventDraft{draft}, nil
}

func (proposal *transactionEnd) FindCommit(snapshot session.Snapshot) (*session.WriteResult, error) {
	for _, entry := range snapshot.Events {
		if entry.Type != compaction.EndEventName {
			continue
		}
		ended, err := compaction.DecodeEnd(entry.Data)
		if err != nil {
			return nil, err
		}
		if ended.CompactionID != proposal.lifecycle.CompactionID {
			continue
		}
		draft, err := proposal.draft()
		if err != nil {
			return nil, err
		}
		if err := draft.CheckCommit(entry); err != nil {
			return nil, fmt.Errorf("compaction: transaction already has a different end: %w", err)
		}
		return &session.WriteResult{
			FirstSeq: entry.Seq,
			NextSeq:  entry.Seq + 1,
			Barrier: session.WriteBarrier{
				SessionID: snapshot.Barrier.SessionID,
				NextSeq:   entry.Seq + 1,
			},
			Events: []session.Event{entry},
		}, nil
	}
	return nil, nil
}

var _ session.IdempotentWrite = (*transactionEnd)(nil)
