package agentloop

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

// ReconcileInput resolves an uncertain claim using an ordered, complete log.
// The runtime must obtain this snapshot after the original request has ended.
// A matching atomic batch applies exactly once; absence preserves pending input.
func (subject *ReactLoopAgent) ReconcileInput(snapshot session.Snapshot) (bool, error) {
	if subject.preparing == nil || subject.preparing.state != inputUnconfirmed {
		return false, ErrState
	}
	boundary := subject.preparing.after
	if snapshot.Barrier.SessionID != subject.identifier || snapshot.Barrier.NextSeq != int64(len(snapshot.Events)) || snapshot.Barrier.NextSeq < boundary {
		return false, errors.New("agentloop: claim recovery requires a complete current Session snapshot")
	}
	for index, entry := range snapshot.Events {
		if entry.Seq != int64(index) {
			return false, errors.New("agentloop: claim recovery sequence is discontinuous")
		}
	}
	drafts := subject.preparing.edit.Drafts
	var confirmed *session.WriteResult
	for offset := boundary; offset < snapshot.Barrier.NextSeq; offset++ {
		if snapshot.Events[offset].Type != agent.InboxSplicedEventName {
			continue
		}
		if confirmed != nil || offset+int64(len(drafts)) > snapshot.Barrier.NextSeq {
			return false, errors.New("agentloop: claim recovery found conflicting Inbox writes")
		}
		next := offset + int64(len(drafts))
		candidate := session.WriteResult{
			FirstSeq: offset,
			NextSeq:  next,
			Barrier: session.WriteBarrier{
				SessionID: subject.identifier,
				NextSeq:   next,
			},
			Events: snapshot.Events[offset:next],
		}
		if err := subject.checkCommit(drafts, candidate); err != nil {
			return false, err
		}
		confirmed = &candidate
		offset = next - 1
	}
	if confirmed == nil {
		subject.preparing = nil
		return false, nil
	}
	if err := subject.ConfirmInput(*confirmed); err != nil {
		return false, err
	}
	return true, nil
}
