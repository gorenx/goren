package agentloop

import (
	"bytes"
	"encoding/json"
	"errors"

	turnstate "github.com/gorenx/goren/agentloop/internal/turn"
	"github.com/gorenx/goren/session"
)

// TurnEnd is the immutable intended end of one Turn. Its detached value can be
// checked against a Session snapshot without accessing the live ReactLoopAgent.
type TurnEnd struct {
	identifier session.SessionID
	number     int64
	draft      session.EventDraft
	payload    json.RawMessage
}

// Drafts builds the first end commit after Session's idempotency lookup.
// The owning WritePlan must also implement FindCommit for retries.
func (ending TurnEnd) Drafts(snapshot session.Snapshot) ([]session.EventDraft, error) {
	latest, err := turnstate.LastNumber(snapshot.Events)
	if err != nil {
		return nil, err
	}
	if latest != ending.number {
		return nil, errors.New("agentloop: Turn end does not belong to the current logged Turn")
	}
	return []session.EventDraft{ending.draft}, nil
}

// Find returns the unique matching end and its prefix, or nil if none exists.
// A conflicting conclusion is an error, never permission to append another end.
func (ending TurnEnd) Find(snapshot session.Snapshot) (*session.WriteResult, error) {
	if ending.identifier == "" || ending.number <= 0 || len(ending.payload) == 0 || snapshot.Barrier.SessionID != ending.identifier {
		return nil, errors.New("agentloop: Turn end requires its owning Session snapshot")
	}
	if snapshot.Barrier.NextSeq != int64(len(snapshot.Events)) {
		return nil, errors.New("agentloop: Turn end lookup requires a complete Session snapshot")
	}
	for index, entry := range snapshot.Events {
		if entry.Seq != int64(index) {
			return nil, errors.New("agentloop: Turn end snapshot sequence is discontinuous")
		}
	}
	var result *session.WriteResult
	for _, entry := range snapshot.Events {
		if entry.Type != session.TurnEndEventName {
			continue
		}
		var boundary struct {
			Turn int64 `json:"turn"`
		}
		if err := json.Unmarshal(entry.Data, &boundary); err != nil {
			return nil, err
		}
		if boundary.Turn != ending.number {
			continue
		}
		if result != nil {
			return nil, errors.New("agentloop: duplicate Turn end in Session log")
		}
		if entry.Seq < 0 || entry.Seq >= snapshot.Barrier.NextSeq {
			return nil, errors.New("agentloop: Turn end is outside the committed prefix")
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, entry.Data); err != nil {
			return nil, err
		}
		if !bytes.Equal(compact.Bytes(), ending.payload) {
			return nil, errors.New("agentloop: logged Turn end conflicts with the prepared conclusion")
		}
		result = &session.WriteResult{
			FirstSeq: entry.Seq,
			NextSeq:  entry.Seq + 1,
			Barrier: session.WriteBarrier{
				SessionID: ending.identifier,
				NextSeq:   entry.Seq + 1,
			},
			Events: []session.Event{entry},
		}
	}
	return result, nil
}

func (TurnEnd) turnOperation() {}
