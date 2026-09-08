package commands

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gorenx/goren/session"
)

// eventWrite is an immutable command fact, independent of its mutable owner.
type eventWrite struct {
	sessionID session.SessionID
	commandID ID
	kind      string
	draft     session.EventDraft
}

func (plan eventWrite) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	if snapshot.Barrier.SessionID != plan.sessionID {
		return nil, errors.New("commands: write belongs to another Session")
	}
	return []session.EventDraft{plan.draft}, nil
}

func (plan eventWrite) FindCommit(snapshot session.Snapshot) (*session.WriteResult, error) {
	if snapshot.Barrier.SessionID != plan.sessionID {
		return nil, errors.New("commands: write belongs to another Session")
	}
	var found *session.WriteResult
	for _, entry := range snapshot.Events {
		if entry.Type != plan.kind {
			continue
		}
		var identity struct {
			CommandID ID `json:"commandId"`
		}
		if err := json.Unmarshal(entry.Data, &identity); err != nil {
			return nil, err
		}
		if identity.CommandID != plan.commandID {
			continue
		}
		if found != nil {
			return nil, errors.New("commands: duplicate lifecycle identity")
		}
		if err := plan.draft.CheckCommit(entry); err != nil {
			return nil, err
		}
		found = &session.WriteResult{
			FirstSeq: entry.Seq,
			NextSeq:  entry.Seq + 1,
			Barrier: session.WriteBarrier{
				SessionID: plan.sessionID,
				NextSeq:   entry.Seq + 1,
			},
			Events: []session.Event{entry},
		}
	}
	return found, nil
}

func (plan eventWrite) check(written session.WriteResult) error {
	if written.Barrier.SessionID != plan.sessionID || written.FirstSeq < 0 || written.NextSeq != written.FirstSeq+1 || written.Barrier.NextSeq != written.NextSeq || len(written.Events) != 1 || written.Events[0].Seq != written.FirstSeq {
		return errors.New("commands: invalid commit identity or range")
	}
	return plan.draft.CheckCommit(written.Events[0])
}
