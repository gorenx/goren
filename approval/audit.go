package approval

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/session"
)

// audit fixes one durable event identity and its complete content. It has no
// mutable receipt cache and can find its commit in a reconstructed Session log.
type audit struct {
	decision  *Decision
	eventType string
	draft     session.EventDraft
}

var _ session.IdempotentWrite = audit{}

func (entry audit) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	if snapshot.Barrier.SessionID != entry.decision.question.SessionID {
		return nil, errors.New("approval: audit belongs to another Session")
	}
	turnSeq, open := openTurn(snapshot.Events)
	if !open || turnSeq != entry.decision.turnSeq {
		return nil, errors.New("approval: audit does not belong to the open Turn")
	}
	if entry.eventType == DecidedEventName {
		preceding, err := entry.find(snapshot, AskedEventName)
		if err != nil {
			return nil, err
		}
		if preceding == nil {
			return nil, errors.New("approval: decided requires a committed asked event")
		}
		draft, err := entry.decision.Asked()
		if err != nil {
			return nil, err
		}
		err = draft.ValidateEvent(*preceding)
		if err != nil {
			return nil, err
		}
	}
	return []session.EventDraft{entry.draft}, nil
}

func (entry audit) FindCommit(snapshot session.Snapshot) (*session.WriteResult, error) {
	if snapshot.Barrier.SessionID != entry.decision.question.SessionID {
		return nil, errors.New("approval: audit belongs to another Session")
	}
	previous, err := entry.find(snapshot, entry.eventType)
	if err != nil || previous == nil {
		return nil, err
	}
	err = entry.draft.ValidateEvent(*previous)
	if err != nil {
		return nil, err
	}
	return &session.WriteResult{
		FirstSeq: previous.Seq,
		NextSeq:  previous.Seq + 1,
		Barrier: session.WriteBarrier{
			SessionID: snapshot.Barrier.SessionID,
			NextSeq:   previous.Seq + 1,
		},
		Events: []session.Event{*previous},
	}, nil
}

func (entry audit) find(snapshot session.Snapshot, eventType string) (*session.Event, error) {
	for _, candidate := range snapshot.Events {
		if candidate.Type != eventType {
			continue
		}
		var payload struct {
			ID RequestID `json:"id"`
		}
		err := json.Unmarshal(candidate.Data, &payload)
		if err != nil {
			return nil, err
		}
		if payload.ID == entry.decision.question.ID {
			if candidate.Seq <= entry.decision.turnSeq {
				return nil, errors.New("approval: audit identity predates its Turn")
			}
			return &candidate, nil
		}
	}
	return nil, nil
}

func (entry audit) validateReceipt(receipt session.WriteResult) error {
	if receipt.Barrier.SessionID != entry.decision.question.SessionID ||
		len(receipt.Events) != 1 ||
		receipt.FirstSeq <= entry.decision.turnSeq ||
		receipt.FirstSeq != receipt.Events[0].Seq ||
		receipt.NextSeq != receipt.FirstSeq+1 ||
		receipt.Barrier.NextSeq != receipt.NextSeq {
		return errors.New("approval: invalid audit commitment receipt")
	}
	return entry.draft.ValidateEvent(receipt.Events[0])
}
