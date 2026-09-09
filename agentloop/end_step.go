package agentloop

import (
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/session"
)

// Drafts permits the first end only for the currently open logged Step.
// Session must also call Find through its IdempotentWrite contract.
func (ending EndStep) Drafts(snapshot session.Snapshot) ([]session.EventDraft, error) {
	previous, open, err := ending.inspect(snapshot)
	if err != nil {
		return nil, err
	}
	if previous != nil || !open {
		return nil, errors.New("agentloop: Step end requires its open logged Step")
	}
	return []session.EventDraft{ending.draft}, nil
}

// Find returns the original Step end acknowledgement after a lost reply.
func (ending EndStep) Find(snapshot session.Snapshot) (*session.WriteResult, error) {
	previous, _, err := ending.inspect(snapshot)
	return previous, err
}

func (ending EndStep) inspect(snapshot session.Snapshot) (*session.WriteResult, bool, error) {
	if ending.identifier == "" ||
		ending.position.Turn <= 0 ||
		ending.position.Step <= 0 ||
		snapshot.Barrier.SessionID != ending.identifier ||
		snapshot.Barrier.NextSeq != int64(len(snapshot.Events)) {
		return nil, false, errors.New("agentloop: Step end requires its complete Session snapshot")
	}
	var previous *session.WriteResult
	started := false
	open := false
	for index, entry := range snapshot.Events {
		if entry.Seq != int64(index) {
			return nil, false, errors.New("agentloop: Step end snapshot sequence is discontinuous")
		}
		if entry.Type == session.TurnStartEventName || entry.Type == session.TurnEndEventName {
			open = false
		}
		if entry.Type != session.StepStartEventName && entry.Type != session.StepEndEventName {
			continue
		}
		var position session.StepPosition
		err := json.Unmarshal(entry.Data, &position)
		if err != nil {
			return nil, false, err
		}
		if position != ending.position {
			open = false
			continue
		}
		if entry.Type == session.StepStartEventName {
			if started {
				return nil, false, errors.New("agentloop: duplicate Step start in Session log")
			}
			started = true
			open = true
			continue
		}
		if !open || previous != nil {
			return nil, false, errors.New("agentloop: Step end has no unique open start")
		}
		err = ending.draft.ValidateEvent(entry)
		if err != nil {
			return nil, false, err
		}
		previous = &session.WriteResult{
			FirstSeq: entry.Seq,
			NextSeq:  entry.Seq + 1,
			Barrier: session.WriteBarrier{
				SessionID: ending.identifier,
				NextSeq:   entry.Seq + 1,
			},
			Events: []session.Event{entry},
		}
		open = false
	}
	return previous, open, nil
}
