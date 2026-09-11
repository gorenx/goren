package agentloop

import (
	"encoding/json"
	"errors"

	turnstate "github.com/gorenx/goren/agentloop/internal/turn"
	"github.com/gorenx/goren/session"
)

// TurnStart is the immutable beginning of one accepted Turn, detached from its
// execution owner. Session evaluates its identity and draft atomically on retry.
type TurnStart struct {
	identifier session.SessionID
	number     int64
	draft      session.EventDraft
}

// Find locates the original commit by Turn number and rejects conflicting facts.
func (beginning TurnStart) Find(snapshot session.Snapshot) (*session.WriteResult, error) {
	if beginning.identifier == "" || beginning.number <= 0 || snapshot.Barrier.SessionID != beginning.identifier || snapshot.Barrier.NextSeq != int64(len(snapshot.Events)) {
		return nil, errors.New("agentloop: Turn start requires its complete Session snapshot")
	}
	var result *session.WriteResult
	for index, entry := range snapshot.Events {
		if entry.Seq != int64(index) {
			return nil, errors.New("agentloop: Turn start snapshot is discontinuous")
		}
		if entry.Type != session.TurnStartEventName {
			continue
		}
		var started session.TurnStart
		if err := json.Unmarshal(entry.Data, &started); err != nil {
			return nil, err
		}
		if started.Turn != beginning.number {
			continue
		}
		if result != nil {
			return nil, errors.New("agentloop: duplicate Turn starts")
		}
		if err := beginning.draft.ValidateEvent(entry); err != nil {
			return nil, err
		}
		result = &session.WriteResult{
			FirstSeq: entry.Seq,
			NextSeq:  entry.Seq + 1,
			Barrier: session.WriteBarrier{
				SessionID: beginning.identifier,
				NextSeq:   entry.Seq + 1,
			},
			Events: []session.Event{entry},
		}
	}
	return result, nil
}

// Drafts builds the first commit after Session has checked Find for a retry.
func (beginning TurnStart) Drafts(snapshot session.Snapshot) ([]session.EventDraft, error) {
	if snapshot.Barrier.SessionID != beginning.identifier {
		return nil, errors.New("agentloop: Turn start Session mismatch")
	}
	last, err := turnstate.LastNumber(snapshot.Events)
	if err != nil {
		return nil, err
	}
	if last+1 != beginning.number {
		return nil, errors.New("agentloop: Turn start is not the next Turn")
	}
	return []session.EventDraft{beginning.draft}, nil
}
