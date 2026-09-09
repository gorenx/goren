package basic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/session"
)

// transactionStart proposes the start of one compaction transaction with a fixed identity.
// It never caches a receipt or mutates the Session snapshot.
type transactionStart struct {
	lifecycle compaction.Start
	requested compaction.SurfaceRange
}

func newTransactionStart(lifecycle compaction.Start, requested compaction.SurfaceRange) (*transactionStart, error) {
	encoded, err := json.Marshal(lifecycle)
	if err != nil {
		return nil, err
	}
	detached, err := compaction.DecodeStart(encoded)
	if err != nil {
		return nil, err
	}
	return &transactionStart{
		lifecycle: detached,
		requested: requested,
	}, nil
}

func (proposal *transactionStart) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	if _, err := locateRegion(snapshot, proposal.requested); err != nil {
		return nil, err
	}
	state, err := compaction.InspectLog(snapshot.Events)
	if err != nil {
		return nil, err
	}
	if state.Attempt != nil {
		return nil, &compaction.ManualError{
			Code:    compaction.ManualErrorBusy,
			Message: "compaction: another transaction is still open",
		}
	}
	if proposal.lifecycle.Turn == nil {
		if state.OpenTurn != nil {
			return nil, &compaction.ManualError{
				Code:    compaction.ManualErrorBusy,
				Message: "manual compaction requires a Session without an open Turn",
			}
		}
	} else if state.OpenTurn == nil || *state.OpenTurn != *proposal.lifecycle.Turn {
		return nil, errors.New("compaction: automatic transaction does not belong to the open Turn")
	}
	draft, err := session.NewEventDraft(compaction.StartEvent, proposal.lifecycle)
	if err != nil {
		return nil, err
	}
	return []session.EventDraft{draft}, nil
}

func (proposal *transactionStart) FindCommit(snapshot session.Snapshot) (*session.WriteResult, error) {
	for _, entry := range snapshot.Events {
		if entry.Type != compaction.StartEventName {
			continue
		}
		started, err := compaction.DecodeStart(entry.Data)
		if err != nil {
			return nil, err
		}
		if started.CompactionID != proposal.lifecycle.CompactionID {
			continue
		}
		draft, err := session.NewEventDraft(compaction.StartEvent, proposal.lifecycle)
		if err != nil {
			return nil, err
		}
		if err := draft.ValidateEvent(entry); err != nil {
			return nil, fmt.Errorf("compaction: start identity conflicts with committed fields: %w", err)
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

var _ session.IdempotentWrite = (*transactionStart)(nil)
